package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"backend/configs"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// BedrockFreeTextRequest is one free-text answer to be scored.
// Mirrors assessments.FreeTextRequest to avoid import cycles.
type BedrockFreeTextRequest struct {
	QuestionID string
	Prompt     string
	Rubric     string
	Answer     string
	MaxPoints  int
}

// BedrockFreeTextResult is the model's scoring plus the narrative artefacts.
// Mirrors assessments.FreeTextResult to avoid import cycles.
type BedrockFreeTextResult struct {
	Scores         map[string]int
	Feedback       string
	AssessorReport string
	Prompt         string
	Raw            string
}

// BedrockScorer implements the Scorer interface (defined in assessments)
// using AWS Bedrock InvokeModel. When env vars are empty it returns an error
// so the caller falls back to human review.
type BedrockScorer struct {
	client  *bedrockruntime.Client
	modelID string
}

// NewBedrockScorer creates a scorer or returns an error if configuration is missing.
func NewBedrockScorer(ctx context.Context) (*BedrockScorer, error) {
	region := configs.BedrockRegion()
	modelID := configs.BedrockModelID()
	if region == "" || modelID == "" {
		return nil, errors.New("bedrock: BEDROCK_REGION and BEDROCK_MODEL_ID must be set")
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("bedrock: loading AWS config: %w", err)
	}

	client := bedrockruntime.NewFromConfig(cfg)
	return &BedrockScorer{client: client, modelID: modelID}, nil
}

// ScoreFreeText scores a batch of free-text answers against their rubrics.
// It builds a single prompt containing all questions, invokes the model, and
// parses the structured response.
func (s *BedrockScorer) ScoreFreeText(ctx context.Context, requests []BedrockFreeTextRequest) (BedrockFreeTextResult, error) {
	if len(requests) == 0 {
		return BedrockFreeTextResult{Scores: map[string]int{}}, nil
	}

	prompt := buildScoringPrompt(requests)

	body, err := json.Marshal(map[string]interface{}{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        4096,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return BedrockFreeTextResult{}, fmt.Errorf("bedrock: marshaling request: %w", err)
	}

	output, err := s.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     &s.modelID,
		ContentType: strPtr("application/json"),
		Accept:      strPtr("application/json"),
		Body:        body,
	})
	if err != nil {
		return BedrockFreeTextResult{}, fmt.Errorf("bedrock: invoke model: %w", err)
	}

	result, err := parseScoringResponse(output.Body, requests)
	if err != nil {
		return BedrockFreeTextResult{}, fmt.Errorf("bedrock: parsing response: %w", err)
	}

	// Persist prompt and raw response for audit.
	result.Prompt = prompt
	result.Raw = string(output.Body)

	return result, nil
}

func buildScoringPrompt(requests []BedrockFreeTextRequest) string {
	var sb strings.Builder
	sb.WriteString("You are an expert trade skills assessor. Score each answer below against its rubric.\n\n")
	sb.WriteString("For each question, provide:\n")
	sb.WriteString("1. A score from 0 to max_points\n")
	sb.WriteString("2. Brief feedback for the artisan (encouraging, specific)\n")
	sb.WriteString("3. A technical note for the assessor reviewing this result\n\n")
	sb.WriteString("Respond in JSON format:\n")
	sb.WriteString(`{"scores": {"<question_id>": <score>, ...}, "feedback": "<artisan feedback>", "assessor_report": "<assessor notes>"}`)
	sb.WriteString("\n\n---\n\n")

	for i, req := range requests {
		fmt.Fprintf(&sb, "Question %d (ID: %s, max_points: %d):\n", i+1, req.QuestionID, req.MaxPoints)
		fmt.Fprintf(&sb, "Prompt: %s\n", req.Prompt)
		fmt.Fprintf(&sb, "Rubric: %s\n", req.Rubric)
		fmt.Fprintf(&sb, "Answer: %s\n\n", req.Answer)
	}

	return sb.String()
}

type bedrockMessage struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

type scoringOutput struct {
	Scores         map[string]int `json:"scores"`
	Feedback       string         `json:"feedback"`
	AssessorReport string         `json:"assessor_report"`
}

func parseScoringResponse(body []byte, requests []BedrockFreeTextRequest) (BedrockFreeTextResult, error) {
	var msg bedrockMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return BedrockFreeTextResult{}, fmt.Errorf("unmarshal response envelope: %w", err)
	}

	if len(msg.Content) == 0 {
		return BedrockFreeTextResult{}, errors.New("empty content in response")
	}

	text := msg.Content[0].Text
	// Strip markdown code fences if present.
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if idx := strings.Index(text[3:], "\n"); idx >= 0 {
			text = text[3+idx+1:]
		}
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
	}

	var output scoringOutput
	if err := json.Unmarshal([]byte(text), &output); err != nil {
		return BedrockFreeTextResult{}, fmt.Errorf("unmarshal scoring JSON: %w", err)
	}

	// Clamp scores to [0, max_points].
	maxByID := make(map[string]int, len(requests))
	for _, req := range requests {
		maxByID[req.QuestionID] = req.MaxPoints
	}
	for id, score := range output.Scores {
		if max, ok := maxByID[id]; ok {
			if score < 0 {
				output.Scores[id] = 0
			} else if score > max {
				output.Scores[id] = max
			}
		}
	}

	return BedrockFreeTextResult{
		Scores:         output.Scores,
		Feedback:       output.Feedback,
		AssessorReport: output.AssessorReport,
	}, nil
}

func strPtr(s string) *string { return &s }
