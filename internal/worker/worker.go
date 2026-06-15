// Package worker runs a single pipeline stage as a claim -> process -> commit loop.
// Workers are stateless; run as many replicas as you like. SKIP LOCKED hands each
// replica a disjoint slice of work with zero inter-worker coordination.
package worker

import (
	"context"
	"log"
	"time"

	"github.com/vkhod/entity-pipeline/internal/llm"
	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
	"github.com/vkhod/entity-pipeline/internal/queue"
)

type Worker struct {
	queue        queue.WorkQueue
	extractor    nlp.Extractor
	classifier   llm.Classifier
	batch        int
	pollInterval time.Duration
	backoff      time.Duration
}

func New(q queue.WorkQueue, ex nlp.Extractor, cl llm.Classifier, batch int, poll, backoff time.Duration) *Worker {
	return &Worker{queue: q, extractor: ex, classifier: cl, batch: batch, pollInterval: poll, backoff: backoff}
}

// RunExtraction loops claiming pending documents and extracting their tokens atomically.
func (w *Worker) RunExtraction(ctx context.Context) {
	log.Println("extraction worker started")
	for {
		if ctx.Err() != nil {
			return
		}
		claimed, err := w.queue.ClaimAndProcessDocument(ctx,
			func(ctx context.Context, doc model.Document) ([]nlp.Entity, error) {
				entities, err := w.extractor.Extract(ctx, doc.SourceText)
				if err == nil {
					log.Printf("[extraction] %s: extracted %d tokens", doc.ID, len(entities))
				}
				return entities, err
			})
		switch {
		case err != nil:
			log.Printf("extraction error (will retry): %v", err)
			w.sleep(ctx, w.backoff)
		case !claimed:
			w.sleep(ctx, w.pollInterval)
		default:
			// claimed and processed; loop immediately to drain any backlog
		}
	}
}

// RunClassification loops claiming batches of extracted tokens and classifying them.
func (w *Worker) RunClassification(ctx context.Context) {
	log.Println("classification worker started")
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := w.queue.ClaimAndProcessTokens(ctx, w.batch,
			func(ctx context.Context, tokens []model.Token) ([]queue.ClassifiedToken, error) {
				ents := make([]nlp.Entity, len(tokens))
				for i, t := range tokens {
					ents[i] = nlp.Entity{
						Text: t.Text, Type: t.NLPType, Page: t.Page,
						Sentence: t.Sentence, CharOffset: t.CharOffset,
					}
				}
				results, err := w.classifier.Classify(ctx, ents)
				if err != nil {
					return nil, err
				}
				out := make([]queue.ClassifiedToken, len(tokens))
				for i := range tokens {
					out[i] = queue.ClassifiedToken{
						TokenID:    tokens[i].ID,
						Category:   results[i].Category,
						Confidence: results[i].Confidence,
						Reasoning:  results[i].Reasoning,
					}
				}
				// Log per-document so progress is readable even when workers are scaled.
				counts := make(map[string]int, len(tokens))
				for _, t := range tokens {
					counts[t.DocumentID]++
				}
				for docID, n := range counts {
					log.Printf("[classification] %s: classified batch of %d", docID, n)
				}
				return out, nil
			})
		switch {
		case err != nil:
			log.Printf("classification error (will retry): %v", err)
			w.sleep(ctx, w.backoff)
		case n == 0:
			w.sleep(ctx, w.pollInterval)
		default:
		}
	}
}

func (w *Worker) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
