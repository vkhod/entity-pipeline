// Package queue defines the WorkQueue port — the seam between the pipeline and its
// transport. The POC implements it over Postgres (SELECT ... FOR UPDATE SKIP LOCKED).
// It can later be backed by a broker (Pub/Sub, Kafka, NATS); at that point the
// txn-scoped "claim and process" below becomes claim-then-release with explicit acks
// (transport and processing model are coupled — not a zero-change swap).
package queue

import (
	"context"

	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
)

// ClassifiedToken is the result of classifying one claimed token.
type ClassifiedToken struct {
	TokenID    int64
	Category   model.Category
	Confidence float64
	Reasoning  string
}

// WorkQueue hands work to stages. Each method claims work and runs the supplied callback
// inside the same unit of work; the implementation commits only if the callback succeeds,
// which is what gives automatic crash recovery (a failed/panicked callback rolls back and
// the work becomes claimable again).
type WorkQueue interface {
	// ClaimAndProcessDocument claims one pending document and runs extraction atomically.
	// `extract` returns the entities to persist. Returns claimed=false when no work is available.
	ClaimAndProcessDocument(
		ctx context.Context,
		extract func(ctx context.Context, doc model.Document) ([]nlp.Entity, error),
	) (claimed bool, err error)

	// ClaimAndProcessTokens claims up to `batch` extracted tokens and runs classification.
	// `classify` returns one result per token. Returns the number of tokens claimed.
	ClaimAndProcessTokens(
		ctx context.Context,
		batch int,
		classify func(ctx context.Context, tokens []model.Token) ([]ClassifiedToken, error),
	) (claimedCount int, err error)
}
