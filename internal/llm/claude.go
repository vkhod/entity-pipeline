package llm

import (
	"context"
	"errors"

	"github.com/vkhod/entity-pipeline/internal/nlp"
)

// ClaudeClassifier is a real classifier backed by the Anthropic Messages API.
//
// SKELETON — wire this tomorrow. It is selected by CLASSIFIER=claude.
// Haiku is a sensible cost-effective default for a short classification task.
type ClaudeClassifier struct {
	apiKey string
	model  string
	// client *anthropic.Client  // (add the SDK client here)
}

func NewClaudeClassifier(apiKey, model string) *ClaudeClassifier {
	return &ClaudeClassifier{apiKey: apiKey, model: model}
}

func (c *ClaudeClassifier) Classify(ctx context.Context, tokens []nlp.Entity) ([]Result, error) {
	// TODO(tomorrow): implement real classification.
	//  1. Build ONE prompt for the whole batch: instruct the model to classify each token
	//     into COMPANY | PERSON | ADDRESS | DATE | UNKNOWN and return a JSON array
	//     [{ "category": ..., "confidence": 0-1, "reasoning": "..." }] in input order.
	//     Give it the token text and its NLP type as context.
	//  2. Call the Anthropic Messages API (anthropic-sdk-go is least boilerplate),
	//     model = c.model, temperature 0 for determinism, a small max_tokens.
	//  3. Parse the JSON array; on length mismatch or parse error, fall back to UNKNOWN
	//     for the affected tokens (never fail the whole batch on one bad token).
	//  4. Wrap the call in retry + exponential backoff for 429 / 5xx; respect rate limits.
	//  5. Cost: one request per batch (not per token); keep batches modest.
	//
	// NOTE on locking: the worker holds its claim transaction across this call (txn-scoped
	// model), which is fine at demo scale. For production, switch the worker to
	// claim-then-release so DB locks are not held across the network round-trip.
	_ = c.apiKey
	return nil, errors.New("ClaudeClassifier not implemented yet; run with CLASSIFIER=mock")
}
