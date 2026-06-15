package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
)

// MockClassifier maps NLP entity types to categories deterministically. The optional
// perTokenDelay makes classification take observable time so /status shows real-time
// progress during the demo — it is NOT representative of real latency.
type MockClassifier struct {
	perTokenDelay time.Duration
}

func NewMockClassifier(perTokenDelay time.Duration) *MockClassifier {
	return &MockClassifier{perTokenDelay: perTokenDelay}
}

func (c *MockClassifier) Classify(ctx context.Context, tokens []nlp.Entity) ([]Result, error) {
	results := make([]Result, len(tokens))
	for i, t := range tokens {
		if c.perTokenDelay > 0 {
			select {
			case <-time.After(c.perTokenDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		cat := categoryFor(t.Type)
		results[i] = Result{
			Category:   cat,
			Confidence: 0.95,
			Reasoning:  fmt.Sprintf("mock: NLP type %q maps to %s", t.Type, cat),
		}
	}
	return results, nil
}

func categoryFor(t model.EntityType) model.Category {
	switch t {
	case "PERSON":
		return model.CategoryPerson
	case "ORG":
		return model.CategoryCompany
	case "ADDRESS", "GPE", "LOC":
		return model.CategoryAddress
	case "DATE":
		return model.CategoryDate
	default:
		return model.CategoryUnknown
	}
}
