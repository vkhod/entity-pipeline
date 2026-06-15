// Package llm defines the classification contract and its implementations
// (a mock by default, and a real Anthropic-backed classifier you can enable).
package llm

import (
	"context"

	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
)

// Result is the classification output for a single token.
type Result struct {
	Category   model.Category
	Confidence float64
	Reasoning  string
}

// Classifier is the contract for the classification stage. The worker claims a batch of
// tokens and calls Classify once per batch; results must be returned in input order.
// A real adapter should send the whole batch in a single request for cost efficiency.
type Classifier interface {
	Classify(ctx context.Context, tokens []nlp.Entity) ([]Result, error)
}
