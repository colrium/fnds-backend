package cvparser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// NewBedrockClient creates a Bedrock Runtime client for the given AWS
// region, using the same default credential chain as the S3 client (see
// files.NewS3Store).
func NewBedrockClient(ctx context.Context, region string) (*bedrockruntime.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration: %w", err)
	}
	return bedrockruntime.NewFromConfig(awsCfg), nil
}

// Parser defines the interface for extracting structured data from CV documents.
type Parser interface {
	// Extract parses the given document bytes and returns structured
	// extraction results. mimeType is the uploaded file's stored MIME type
	// (e.g. "application/pdf"), used to tell Bedrock the document's actual
	// format instead of assuming PDF.
	Extract(ctx context.Context, docBytes []byte, mimeType string) (*ExtractionResult, error)
}

// documentFormatByMimeType maps the MIME types CV uploads are validated
// against (files.ValidateCVUpload) to the Bedrock Converse document formats
// they actually are, so a DOCX upload is sent to the model labeled as DOCX
// rather than mislabeled as PDF.
var documentFormatByMimeType = map[string]types.DocumentFormat{
	"application/pdf": types.DocumentFormatPdf,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": types.DocumentFormatDocx,
}

// ErrUnsupportedDocumentFormat is returned when the given MIME type has no
// corresponding Bedrock document format.
var ErrUnsupportedDocumentFormat = errors.New("unsupported document format")

// maxDocumentBytes bounds the PDF sent to Bedrock. The Converse API's
// document content block accepts documents up to 4.5 MB (Req: Bedrock
// Converse document limits), well under the 10 MB upload-time limit, so a
// clear client-side error is surfaced instead of a provider-side rejection.
const maxDocumentBytes = 4*1024*1024 + 500*1024

// extractionToolName is the name of the tool the model is forced to call to
// return its extraction result. Bedrock's Converse API has no direct
// equivalent of an OpenAI-style response_format/json_schema — structured
// output is obtained by defining the schema as a tool and using
// SpecificToolChoice to force the model to call it, which Anthropic Claude
// models support (Req: tool use / function calling).
const extractionToolName = "extract_cv_data"

// BedrockParser implements the Parser interface using Amazon Bedrock's
// Converse API with an Anthropic Claude model, forced into calling the
// extraction tool so its response is constrained to the CV extraction
// schema. The PDF is sent as a native document content block rather than
// base64-encoded in the prompt text, which avoids the ~33% base64 expansion
// that ate into an OpenAI-compatible provider's context window.
type BedrockParser struct {
	client  *bedrockruntime.Client
	modelID string
}

// NewBedrockParser creates a new BedrockParser using the given Bedrock
// Runtime client and model ID (a base model ID, an inference profile ID, or
// their ARNs — see Amazon Bedrock's model ID documentation).
func NewBedrockParser(client *bedrockruntime.Client, modelID string) *BedrockParser {
	return &BedrockParser{client: client, modelID: modelID}
}

// systemPrompt instructs the model how to extract CV data.
const systemPrompt = `You are a CV/resume data extraction assistant. You will receive a PDF document. Extract structured information from it accurately by calling the ` + extractionToolName + ` tool exactly once.

Instructions:
- Extract personal details, education, professional information, work history, languages, and references.
- If a field cannot be found in the document, return null for that field.
- Assign confidence scores between 0 and 1 for each section based on how certain you are about the extraction accuracy.
- If the PDF appears to be password-protected or the content cannot be read, indicate this by setting all confidence scores to 0 and returning null for all fields.
- Do not guess or fabricate information. Only extract what is explicitly stated in the document.`

// Extract parses the given document bytes by sending them to the configured
// Claude model via Bedrock's Converse API, forced to call the extraction
// tool, and returns the extracted CV data. mimeType determines which Bedrock
// document format the bytes are labeled with (see documentFormatByMimeType);
// an unrecognized MIME type is a bug in the upload validation layer letting
// through a type this parser was never told about, not a routine client
// mistake, so it is reported as ErrUnsupportedDocumentFormat rather than
// silently treated as PDF. It handles timeouts, API errors, unreadable
// documents, and password-protected PDFs.
func (p *BedrockParser) Extract(ctx context.Context, docBytes []byte, mimeType string) (*ExtractionResult, error) {
	if len(docBytes) == 0 {
		return nil, ErrPDFUnreadable
	}

	format, ok := documentFormatByMimeType[strings.ToLower(strings.TrimSpace(mimeType))]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDocumentFormat, mimeType)
	}

	// Check size before scanning for password protection: the size check is
	// O(1) while the password scan touches the document bytes, so fail fast
	// on the cheaper constraint first.
	if len(docBytes) > maxDocumentBytes {
		return nil, fmt.Errorf("%w: document size %d bytes exceeds provider limit", ErrPDFTooLarge, len(docBytes))
	}

	// The password-protection heuristic scans for a PDF-specific trailer
	// entry, so it only applies when the document actually is a PDF.
	if format == types.DocumentFormatPdf && isPasswordProtected(docBytes) {
		return nil, ErrPDFPasswordProtected
	}

	toolConfig := &types.ToolConfiguration{
		Tools: []types.Tool{
			&types.ToolMemberToolSpec{
				Value: types.ToolSpecification{
					Name:        aws.String(extractionToolName),
					Description: aws.String("Records structured CV/resume data extracted from the provided document."),
					InputSchema: &types.ToolInputSchemaMemberJson{
						Value: document.NewLazyDocument(cvExtractionInputSchemaDoc),
					},
				},
			},
		},
		ToolChoice: &types.ToolChoiceMemberTool{
			Value: types.SpecificToolChoice{Name: aws.String(extractionToolName)},
		},
	}

	output, err := p.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(p.modelID),
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: systemPrompt},
		},
		Messages: []types.Message{
			{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberDocument{
						Value: types.DocumentBlock{
							Name:   aws.String("cv"),
							Format: format,
							Source: &types.DocumentSourceMemberBytes{Value: docBytes},
						},
					},
					&types.ContentBlockMemberText{
						Value: "Extract structured CV data from the attached document by calling the " + extractionToolName + " tool.",
					},
				},
			},
		},
		ToolConfig: toolConfig,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, ErrParseTimeout
		}
		return nil, fmt.Errorf("%w: %v", ErrParserUnavailable, err)
	}

	switch output.StopReason {
	case types.StopReasonToolUse:
		// Expected path — extract the tool call below.
	case types.StopReasonMaxTokens:
		return nil, fmt.Errorf("%w: response truncated before completion", ErrParserUnavailable)
	case types.StopReasonContentFiltered, types.StopReasonGuardrailIntervened:
		return nil, fmt.Errorf("%w: response blocked by content filter", ErrParserUnavailable)
	case types.StopReasonMalformedToolUse:
		return nil, fmt.Errorf("%w: model produced a malformed tool call", ErrParserUnavailable)
	default:
		// Any other stop reason (e.g. end_turn without a tool call) means the
		// model didn't call the forced tool at all. That's a provider/
		// integration problem, not evidence the PDF itself is unreadable, so
		// it is surfaced as ErrParserUnavailable rather than
		// ErrPDFUnreadable.
		slog.WarnContext(ctx, "Bedrock Converse returned unexpected stop reason",
			"stop_reason", string(output.StopReason), "model_id", p.modelID)
		return nil, fmt.Errorf("%w: unexpected stop reason %q", ErrParserUnavailable, string(output.StopReason))
	}

	message, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return nil, fmt.Errorf("%w: no message in response", ErrParserUnavailable)
	}

	var toolInput document.Interface
	for _, block := range message.Value.Content {
		if toolUse, ok := block.(*types.ContentBlockMemberToolUse); ok {
			toolInput = toolUse.Value.Input
			break
		}
	}
	if toolInput == nil {
		return nil, ErrPDFUnreadable
	}

	// toolInput.UnmarshalSmithyDocument is deliberately not used here: the
	// smithy document decoder matches struct fields by a "document:" tag
	// (falling back to the exact Go field name, e.g. "PersonalDetails"), not
	// by "json:" tags — so against our snake_case tool schema it silently
	// matches nothing and leaves every field zero-valued, with no error.
	// Round-tripping through MarshalSmithyDocument's raw JSON bytes and
	// encoding/json instead makes the existing "json:" tags on
	// ExtractionResult authoritative, the same as everywhere else they're used.
	rawInput, err := toolInput.MarshalSmithyDocument()
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read tool input", ErrParserUnavailable)
	}

	var result ExtractionResult
	if err := json.Unmarshal(rawInput, &result); err != nil {
		return nil, fmt.Errorf("%w: failed to parse extraction result", ErrParserUnavailable)
	}

	// confidence_scores is a required property in the tool schema; a nil
	// value here means the model violated the schema, which is a provider
	// integration issue rather than anything wrong with the PDF.
	if result.ConfidenceScores == nil {
		return nil, fmt.Errorf("%w: model omitted required confidence_scores", ErrParserUnavailable)
	}

	// Check if the result indicates an unreadable PDF (all confidence scores are 0
	// and all fields are nil/empty).
	if isUnreadableResult(&result) {
		return nil, ErrPDFUnreadable
	}

	return &result, nil
}

// isPasswordProtected performs a basic heuristic check on the PDF bytes to
// detect if the document is encrypted/password-protected. It looks for the
// /Encrypt dictionary entry which indicates PDF-level encryption.
func isPasswordProtected(pdfBytes []byte) bool {
	// PDF trailers live at the end of the file. Searching only the last 4 KB
	// avoids false positives from page content that happens to contain
	// "/Encrypt" as literal text.
	trailer := pdfBytes
	if len(trailer) > 4096 {
		trailer = trailer[len(trailer)-4096:]
	}
	return bytes.Contains(trailer, []byte("/Encrypt"))
}

// isUnreadableResult checks if the extraction result indicates that the PDF
// content could not be read (all confidence scores are 0). A nil
// ConfidenceScores is not treated as unreadable — the tool schema requires
// this field, so a nil value means the model violated the schema, which
// callers should surface as a parser error rather than an unreadable PDF.
func isUnreadableResult(result *ExtractionResult) bool {
	if result.ConfidenceScores == nil {
		return false
	}

	scores := result.ConfidenceScores
	return scores.PersonalDetails == 0 &&
		scores.Education == 0 &&
		scores.Professional == 0 &&
		scores.WorkHistory == 0 &&
		scores.Languages == 0 &&
		scores.References == 0
}

// cvExtractionInputSchemaDoc is the parsed form of cvExtractionInputSchema,
// built once at package init instead of on every Extract call since the
// schema never changes.
var cvExtractionInputSchemaDoc map[string]any

func init() {
	if err := json.Unmarshal([]byte(cvExtractionInputSchema), &cvExtractionInputSchemaDoc); err != nil {
		panic(fmt.Sprintf("cvparser: failed to parse embedded tool input schema: %v", err))
	}
}

// cvExtractionInputSchema is the JSON Schema for the extraction tool's input,
// given to Claude via Bedrock's tool-use so its response is constrained to a
// valid, well-typed object matching the ExtractionResult structure.
const cvExtractionInputSchema = `{
  "type": "object",
  "properties": {
    "personal_details": {
      "type": "object",
      "properties": {
        "address": { "type": ["string", "null"] },
        "city": { "type": ["string", "null"] },
        "state": { "type": ["string", "null"] }
      },
      "required": ["address", "city", "state"]
    },
    "education": {
      "type": "object",
      "properties": {
        "highest_level": { "type": ["string", "null"] },
        "certificate": { "type": ["string", "null"] },
        "institution": { "type": ["string", "null"] },
        "other_qualifications": { "type": ["string", "null"] }
      },
      "required": ["highest_level", "certificate", "institution", "other_qualifications"]
    },
    "professional": {
      "type": "object",
      "properties": {
        "professional_body": { "type": ["string", "null"] },
        "skill_acquisition_mode": { "type": ["string", "null"] },
        "years_of_experience": { "type": ["integer", "null"] }
      },
      "required": ["professional_body", "skill_acquisition_mode", "years_of_experience"]
    },
    "work_history": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "employer_name": { "type": ["string", "null"] },
          "phone_number": { "type": ["string", "null"] },
          "location": { "type": ["string", "null"] }
        },
        "required": ["employer_name", "phone_number", "location"]
      }
    },
    "languages": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "language": { "type": ["string", "null"] },
          "proficiency_level": { "type": ["string", "null"] },
          "written_skills": { "type": ["string", "null"] }
        },
        "required": ["language", "proficiency_level", "written_skills"]
      }
    },
    "references": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": { "type": ["string", "null"] },
          "phone_number": { "type": ["string", "null"] },
          "job_title": { "type": ["string", "null"] },
          "email": { "type": ["string", "null"] },
          "location": { "type": ["string", "null"] }
        },
        "required": ["name", "phone_number", "job_title", "email", "location"]
      }
    },
    "confidence_scores": {
      "type": "object",
      "properties": {
        "personal_details": { "type": "number" },
        "education": { "type": "number" },
        "professional": { "type": "number" },
        "work_history": { "type": "number" },
        "languages": { "type": "number" },
        "references": { "type": "number" }
      },
      "required": ["personal_details", "education", "professional", "work_history", "languages", "references"]
    }
  },
  "required": ["personal_details", "education", "professional", "work_history", "languages", "references", "confidence_scores"]
}`
