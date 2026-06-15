// Package nlp defines the extraction (NLP) contract and a rule-based mock implementation.
package nlp

import (
	"context"

	"github.com/vkhod/entity-pipeline/internal/model"
)

// Entity is a candidate entity produced by the extraction stage.
type Entity struct {
	Text       string
	Type       model.EntityType
	Page       int
	Sentence   int
	CharOffset int
}

// Extractor is the contract for the NLP stage. Implementations may be a rule-based mock
// or a real NLP/NER service; the rest of the system depends only on this interface.
type Extractor interface {
	Extract(ctx context.Context, text string) ([]Entity, error)
}
