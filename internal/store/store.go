// Package store is the Postgres-backed source of truth AND work queue. It implements
// queue.WorkQueue (the SKIP-LOCKED claim methods) plus the API read/write methods.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
	"github.com/vkhod/entity-pipeline/internal/queue"
)

// Outcome reports what CreateOrRerun did, so the API can map it to an HTTP status.
type Outcome int

const (
	OutcomeCreated  Outcome = iota // new document accepted        -> 202
	OutcomeReran                   // terminal document reset/reran -> 202
	OutcomeConflict                // document mid-flight; rejected -> 409
)

// TokenFilter holds optional filters for ListTokens.
type TokenFilter struct {
	Classification string // "" = any
	Page           *int   // nil = any
	Status         string // "" = any
	Limit          int
	Offset         int
}

// Store wraps a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// ---- SQL -------------------------------------------------------------------------------

const (
	// ON CONFLICT DO NOTHING lets us detect whether we created a new row (RowsAffected==1)
	// or hit an existing one (RowsAffected==0) without a separate SELECT first.
	// This closes the first-submit race: two concurrent POSTs for the same new document_id
	// both attempt this INSERT; only one wins, the other gets RowsAffected==0 and proceeds
	// to the FOR UPDATE path below.
	sqlInsertPending = `
		INSERT INTO documents (id, source_text, status)
		VALUES ($1, $2, 'pending')
		ON CONFLICT (id) DO NOTHING`

	sqlLockDocument = `SELECT status FROM documents WHERE id = $1 FOR UPDATE`
	sqlDeleteTokens = `DELETE FROM tokens WHERE document_id = $1`
	sqlResetDocument = `
		UPDATE documents
		   SET status='pending', generation=generation+1, source_text=$2,
		       total_tokens=0, classified_count=0,
		       extraction_started_at=NULL, extraction_completed_at=NULL,
		       classification_started_at=NULL, classification_completed_at=NULL,
		       error=NULL, updated_at=now()
		 WHERE id=$1`

	sqlGetDocument = `
		SELECT id, status, generation, source_text,
		       total_tokens, classified_count,
		       extraction_started_at, extraction_completed_at,
		       classification_started_at, classification_completed_at,
		       error, created_at, updated_at
		  FROM documents WHERE id = $1`

	sqlClaimPendingDoc = `
		SELECT id, source_text, generation FROM documents
		 WHERE status='pending'
		 ORDER BY created_at
		 FOR UPDATE SKIP LOCKED
		 LIMIT 1`
	sqlStartExtraction = `UPDATE documents SET extraction_started_at=now() WHERE id=$1`
	sqlInsertToken     = `
		INSERT INTO tokens (document_id, ordinal, text, nlp_type, page, sentence, char_offset, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'extracted')`
	sqlFinishExtraction = `
		UPDATE documents
		   SET total_tokens=$2, extraction_completed_at=now(),
		       classification_started_at=now(), status='classifying', updated_at=now()
		 WHERE id=$1`
	// Zero-entity documents skip classification entirely: nothing to classify means 'completed'.
	sqlFinishExtractionEmpty = `
		UPDATE documents
		   SET total_tokens=0, extraction_completed_at=now(),
		       classification_started_at=now(), classification_completed_at=now(),
		       status='completed', updated_at=now()
		 WHERE id=$1`

	sqlClaimExtractedTokens = `
		SELECT id, document_id, ordinal, text, nlp_type, page, sentence, char_offset
		  FROM tokens
		 WHERE status='extracted'
		 ORDER BY document_id, ordinal
		 FOR UPDATE SKIP LOCKED
		 LIMIT $1`
	sqlUpdateTokenClassified = `
		UPDATE tokens
		   SET classification=$2, confidence=$3, reasoning=$4,
		       status='classified', classified_at=now()
		 WHERE id=$1`
	// Atomic counter bump + race-safe completion flip: the worker whose commit reaches
	// total_tokens also marks the document completed.
	sqlBumpCounterMaybeComplete = `
		UPDATE documents
		   SET classified_count = classified_count + $2,
		       status = CASE WHEN classified_count + $2 >= total_tokens THEN 'completed' ELSE status END,
		       classification_completed_at = CASE WHEN classified_count + $2 >= total_tokens
		                                          THEN now() ELSE classification_completed_at END,
		       updated_at = now()
		 WHERE id=$1`
)

// ---- WorkQueue implementation ----------------------------------------------------------

// ClaimAndProcessDocument: claim one pending doc (SKIP LOCKED) and extract its tokens
// atomically, all in one transaction. The row lock is held until commit, so a crash
// rolls back to zero tokens and the doc stays 'pending' for another worker.
func (s *Store) ClaimAndProcessDocument(
	ctx context.Context,
	extract func(context.Context, model.Document) ([]nlp.Entity, error),
) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var doc model.Document
	err = tx.QueryRow(ctx, sqlClaimPendingDoc).Scan(&doc.ID, &doc.SourceText, &doc.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // no pending work right now
	}
	if err != nil {
		return false, err
	}

	if _, err = tx.Exec(ctx, sqlStartExtraction, doc.ID); err != nil {
		return false, err
	}

	entities, err := extract(ctx, doc)
	if err != nil {
		// Rollback fires via defer; doc stays 'pending' so another worker retries.
		return false, err
	}

	if len(entities) == 0 {
		// Nothing to classify: skip the classifying stage and mark completed immediately.
		if _, err = tx.Exec(ctx, sqlFinishExtractionEmpty, doc.ID); err != nil {
			return false, err
		}
	} else {
		for i, e := range entities {
			if _, err = tx.Exec(ctx, sqlInsertToken,
				doc.ID, i, e.Text, e.Type, e.Page, e.Sentence, e.CharOffset,
			); err != nil {
				return false, err
			}
		}
		if _, err = tx.Exec(ctx, sqlFinishExtraction, doc.ID, len(entities)); err != nil {
			return false, err
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// ClaimAndProcessTokens: claim up to `batch` extracted tokens (SKIP LOCKED), classify
// them, then persist results and bump the per-document counter. Crash before commit
// reverts the rows to 'extracted'.
func (s *Store) ClaimAndProcessTokens(
	ctx context.Context,
	batch int,
	classify func(context.Context, []model.Token) ([]queue.ClassifiedToken, error),
) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Claim and scan tokens inside the transaction so SKIP LOCKED row-locks are held.
	rows, err := tx.Query(ctx, sqlClaimExtractedTokens, batch)
	if err != nil {
		return 0, err
	}
	tokens, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Token, error) {
		var t model.Token
		return t, row.Scan(
			&t.ID, &t.DocumentID, &t.Ordinal, &t.Text, &t.NLPType,
			&t.Page, &t.Sentence, &t.CharOffset,
		)
	})
	if err != nil {
		return 0, err
	}
	if len(tokens) == 0 {
		return 0, nil // no work right now; deferred Rollback is harmless
	}

	results, err := classify(ctx, tokens)
	if err != nil {
		// Rollback via defer; tokens revert to 'extracted' and are re-claimed on next pass.
		return 0, err
	}
	if len(results) != len(tokens) {
		return 0, fmt.Errorf("classifier returned %d results for %d tokens", len(results), len(tokens))
	}

	// Persist classifications. Results are guaranteed in the same order as tokens.
	for i, r := range results {
		if _, err = tx.Exec(ctx, sqlUpdateTokenClassified,
			tokens[i].ID, r.Category, r.Confidence, r.Reasoning,
		); err != nil {
			return 0, err
		}
	}

	// Bump the per-document classified_count. A batch may span multiple documents
	// (e.g. last few tokens of doc-A and first few of doc-B), so we group first.
	countPerDoc := make(map[string]int, len(tokens))
	for _, t := range tokens {
		countPerDoc[t.DocumentID]++
	}
	for docID, n := range countPerDoc {
		if _, err = tx.Exec(ctx, sqlBumpCounterMaybeComplete, docID, n); err != nil {
			return 0, err
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(tokens), nil
}

// ---- API methods -----------------------------------------------------------------------

// CreateOrRerun implements POST /process: create a new pending doc, full-rerun a terminal
// one (delete tokens + reset + bump generation), or reject a mid-flight one — all in one
// transaction serialized on the document row.
func (s *Store) CreateOrRerun(ctx context.Context, id, text string) (model.Document, Outcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Document{}, OutcomeConflict, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Attempt insert first. RowsAffected==1 means a new row; ==0 means the id already exists.
	tag, err := tx.Exec(ctx, sqlInsertPending, id, text)
	if err != nil {
		return model.Document{}, OutcomeConflict, err
	}

	var outcome Outcome
	if tag.RowsAffected() == 1 {
		outcome = OutcomeCreated
	} else {
		// Lock the existing row so no other request can race on it.
		var status model.DocumentStatus
		if err = tx.QueryRow(ctx, sqlLockDocument, id).Scan(&status); err != nil {
			return model.Document{}, OutcomeConflict, err
		}
		switch status {
		case model.StatusPending, model.StatusClassifying:
			// In-flight: reject. Rollback via defer; no write needed.
			return model.Document{}, OutcomeConflict, nil
		default: // completed | failed → full rerun
			if _, err = tx.Exec(ctx, sqlDeleteTokens, id); err != nil {
				return model.Document{}, OutcomeConflict, err
			}
			if _, err = tx.Exec(ctx, sqlResetDocument, id, text); err != nil {
				return model.Document{}, OutcomeConflict, err
			}
			outcome = OutcomeReran
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return model.Document{}, OutcomeConflict, err
	}
	doc, _, err := s.GetDocument(ctx, id)
	return doc, outcome, err
}

func (s *Store) GetDocument(ctx context.Context, id string) (model.Document, bool, error) {
	var doc model.Document
	var errCol *string
	err := s.pool.QueryRow(ctx, sqlGetDocument, id).Scan(
		&doc.ID, &doc.Status, &doc.Generation, &doc.SourceText,
		&doc.TotalTokens, &doc.ClassifiedCount,
		&doc.ExtractionStartedAt, &doc.ExtractionCompletedAt,
		&doc.ClassificationStartedAt, &doc.ClassificationCompletedAt,
		&errCol, &doc.CreatedAt, &doc.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Document{}, false, nil
	}
	if err != nil {
		return model.Document{}, false, err
	}
	if errCol != nil {
		doc.Error = *errCol
	}
	return doc, true, nil
}

func (s *Store) ListTokens(ctx context.Context, id string, f TokenFilter) ([]model.Token, error) {
	args := []any{id}
	q := `SELECT id, document_id, ordinal, text, nlp_type, page, sentence, char_offset,
	             classification, confidence, reasoning, status, created_at, classified_at
	        FROM tokens WHERE document_id = $1`

	if f.Classification != "" {
		args = append(args, f.Classification)
		q += fmt.Sprintf(" AND classification = $%d", len(args))
	}
	if f.Page != nil {
		args = append(args, *f.Page)
		q += fmt.Sprintf(" AND page = $%d", len(args))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}

	args = append(args, f.Limit, f.Offset)
	q += fmt.Sprintf(" ORDER BY ordinal LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []model.Token
	for rows.Next() {
		var t model.Token
		var classification *string
		var reasoning *string
		if err := rows.Scan(
			&t.ID, &t.DocumentID, &t.Ordinal, &t.Text, &t.NLPType,
			&t.Page, &t.Sentence, &t.CharOffset,
			&classification, &t.Confidence, &reasoning, &t.Status,
			&t.CreatedAt, &t.ClassifiedAt,
		); err != nil {
			return nil, err
		}
		if classification != nil {
			cat := model.Category(*classification)
			t.Classification = &cat
		}
		if reasoning != nil {
			t.Reasoning = *reasoning
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}
