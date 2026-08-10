// Package scoring provides concrete Scorer implementations for the assessments
// pipeline. It lives in its own package to avoid import cycles between
// assessments and integrations.
package scoring

import (
	"context"

	"backend/assessments"
	"backend/integrations"
)

// bedrockAdapter wraps integrations.BedrockScorer to satisfy the
// assessments.Scorer interface, bridging the mirrored types.
type bedrockAdapter struct {
	scorer *integrations.BedrockScorer
}

// NewBedrockScorer creates an assessments.Scorer backed by AWS Bedrock.
// Returns an error if required env vars (BEDROCK_REGION, BEDROCK_MODEL_ID) are
// not configured, signalling the caller to fall back to human review.
func NewBedrockScorer(ctx context.Context) (assessments.Scorer, error) {
	s, err := integrations.NewBedrockScorer(ctx)
	if err != nil {
		return nil, err
	}
	return &bedrockAdapter{scorer: s}, nil
}

func (a *bedrockAdapter) ScoreFreeText(ctx context.Context, requests []assessments.FreeTextRequest) (assessments.FreeTextResult, error) {
	reqs := make([]integrations.BedrockFreeTextRequest, len(requests))
	for i, r := range requests {
		reqs[i] = integrations.BedrockFreeTextRequest{
			QuestionID: r.QuestionID,
			Prompt:     r.Prompt,
			Rubric:     r.Rubric,
			Answer:     r.Answer,
			MaxPoints:  r.MaxPoints,
		}
	}

	result, err := a.scorer.ScoreFreeText(ctx, reqs)
	if err != nil {
		return assessments.FreeTextResult{}, err
	}

	return assessments.FreeTextResult{
		Scores:         result.Scores,
		Feedback:       result.Feedback,
		AssessorReport: result.AssessorReport,
		Prompt:         result.Prompt,
		Raw:            result.Raw,
	}, nil
}
