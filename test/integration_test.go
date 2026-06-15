// Package test holds integration tests that spin up a real Postgres via testcontainers.
// Run with: go test ./test/... (requires Docker).
package test

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/vkhod/entity-pipeline/internal/llm"
	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
	"github.com/vkhod/entity-pipeline/internal/queue"
	"github.com/vkhod/entity-pipeline/internal/store"
)

// ---- package-level setup ---------------------------------------------------------------

var (
	testDSN  string
	testPool *pgxpool.Pool
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Resolve schema.sql relative to this source file (robust regardless of where go test runs).
	_, thisFile, _, _ := runtime.Caller(0)
	schemaPath := filepath.Join(filepath.Dir(thisFile), "../migrations/schema.sql")

	pgContainer, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("pipeline"),
		tcpostgres.WithUsername("pipeline"),
		tcpostgres.WithPassword("pipeline"),
		tcpostgres.WithInitScripts(schemaPath),
	)
	if err != nil {
		log.Fatalf("start postgres container: %v", err)
	}
	defer func() {
		if err := pgContainer.Terminate(ctx); err != nil {
			log.Printf("terminate postgres container: %v", err)
		}
	}()

	testDSN, err = pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("get container DSN: %v", err)
	}

	testPool, err = pgxpool.New(ctx, testDSN)
	if err != nil {
		log.Fatalf("create test pool: %v", err)
	}
	defer testPool.Close()

	// Postgres opens its port before finishing docker-entrypoint-initdb.d scripts.
	// Retry until the pool can actually accept queries (schema may still be applying).
	for i := 0; i < 30; i++ {
		if pingErr := testPool.Ping(ctx); pingErr == nil {
			break
		}
		if i == 29 {
			log.Fatal("postgres not ready after 15s")
		}
		time.Sleep(500 * time.Millisecond)
	}

	os.Exit(m.Run())
}

// ---- helpers ---------------------------------------------------------------------------

// newStore creates a Store connected to the test container, registered for cleanup.
func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// truncate wipes all rows between tests so each test starts clean.
func truncate(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), "TRUNCATE documents CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// extractFn wraps MockExtractor into the callback signature ClaimAndProcessDocument expects.
func extractFn(ctx context.Context, doc model.Document) ([]nlp.Entity, error) {
	return nlp.NewMockExtractor().Extract(ctx, doc.SourceText)
}

// classifyFn wraps MockClassifier into the callback signature ClaimAndProcessTokens expects.
func classifyFn(ctx context.Context, tokens []model.Token) ([]queue.ClassifiedToken, error) {
	c := llm.NewMockClassifier(0) // no delay in tests
	ents := make([]nlp.Entity, len(tokens))
	for i, t := range tokens {
		ents[i] = nlp.Entity{Text: t.Text, Type: t.NLPType, Page: t.Page, Sentence: t.Sentence, CharOffset: t.CharOffset}
	}
	results, err := c.Classify(ctx, ents)
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
	return out, nil
}

// classifyAll drains all extracted tokens to completion.
func classifyAll(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	for {
		n, err := st.ClaimAndProcessTokens(ctx, 100, classifyFn)
		if err != nil {
			t.Fatalf("ClaimAndProcessTokens: %v", err)
		}
		if n == 0 {
			break
		}
	}
}

// ---- CreateOrRerun tests ---------------------------------------------------------------

func TestCreateOrRerun_Create(t *testing.T) {
	truncate(t)
	st := newStore(t)
	ctx := context.Background()

	doc, outcome, err := st.CreateOrRerun(ctx, "doc-1", "some text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != store.OutcomeCreated {
		t.Errorf("outcome = %v, want OutcomeCreated", outcome)
	}
	if doc.ID != "doc-1" {
		t.Errorf("doc.ID = %q, want %q", doc.ID, "doc-1")
	}
	if doc.Status != model.StatusPending {
		t.Errorf("doc.Status = %q, want %q", doc.Status, model.StatusPending)
	}
	if doc.Generation != 1 {
		t.Errorf("doc.Generation = %d, want 1", doc.Generation)
	}
}

func TestCreateOrRerun_ConflictWhenInFlight(t *testing.T) {
	truncate(t)
	st := newStore(t)
	ctx := context.Background()

	// First submit succeeds.
	if _, _, err := st.CreateOrRerun(ctx, "doc-1", "some text"); err != nil {
		t.Fatalf("first CreateOrRerun: %v", err)
	}
	// Doc is now 'pending' (in-flight). Second submit must be rejected.
	_, outcome, err := st.CreateOrRerun(ctx, "doc-1", "some text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome != store.OutcomeConflict {
		t.Errorf("outcome = %v, want OutcomeConflict", outcome)
	}
}

func TestCreateOrRerun_Rerun(t *testing.T) {
	truncate(t)
	st := newStore(t)
	ctx := context.Background()

	// Create and drive to completion.
	if _, _, err := st.CreateOrRerun(ctx, "doc-1", "Acme Corporation on January 15, 2024."); err != nil {
		t.Fatalf("initial CreateOrRerun: %v", err)
	}
	if _, err := st.ClaimAndProcessDocument(ctx, extractFn); err != nil {
		t.Fatalf("ClaimAndProcessDocument: %v", err)
	}
	classifyAll(t, st)

	doc, _, _ := st.GetDocument(ctx, "doc-1")
	if doc.Status != model.StatusCompleted {
		t.Fatalf("doc not completed after full cycle (status=%q)", doc.Status)
	}

	// Rerun on a completed document.
	doc2, outcome, err := st.CreateOrRerun(ctx, "doc-1", "new text")
	if err != nil {
		t.Fatalf("rerun CreateOrRerun: %v", err)
	}
	if outcome != store.OutcomeReran {
		t.Errorf("outcome = %v, want OutcomeReran", outcome)
	}
	if doc2.Generation != 2 {
		t.Errorf("generation = %d, want 2 after rerun", doc2.Generation)
	}
	if doc2.Status != model.StatusPending {
		t.Errorf("status = %q, want pending after rerun", doc2.Status)
	}

	// Old tokens should be gone.
	tokens, err := st.ListTokens(ctx, "doc-1", store.TokenFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens after rerun, got %d", len(tokens))
	}
}

func TestCreateOrRerun_ConcurrentFirstSubmit(t *testing.T) {
	// Two goroutines submit the same brand-new document_id simultaneously.
	// Expected: exactly one OutcomeCreated and one OutcomeConflict — no errors and no panics.
	truncate(t)
	st := newStore(t)
	ctx := context.Background()

	type result struct {
		outcome store.Outcome
		err     error
	}
	results := make([]result, 2)

	// Channel used as a starting gun so both goroutines hit CreateOrRerun at the same time.
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-ready
			_, results[i].outcome, results[i].err = st.CreateOrRerun(ctx, "doc-race", "text")
		}(i)
	}
	close(ready) // release both goroutines simultaneously
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, r.err)
		}
	}

	counts := map[store.Outcome]int{}
	for _, r := range results {
		counts[r.outcome]++
	}
	if counts[store.OutcomeCreated] != 1 || counts[store.OutcomeConflict] != 1 {
		t.Errorf("expected 1×Created + 1×Conflict; got Created=%d Conflict=%d",
			counts[store.OutcomeCreated], counts[store.OutcomeConflict])
	}
}

// ---- Full pipeline cycle ---------------------------------------------------------------

func TestFullCycle(t *testing.T) {
	truncate(t)
	st := newStore(t)
	ctx := context.Background()

	const text = `Acme Corporation announced on January 15, 2024 that Sarah Johnson will join as Chief
Technology Officer. The company is headquartered at 500 Market Street in San Francisco.
Michael Chen, the outgoing officer, will advise Globex Industries starting March 1, 2024.`

	// 1. Submit document.
	_, _, err := st.CreateOrRerun(ctx, "doc-cycle", text)
	if err != nil {
		t.Fatalf("CreateOrRerun: %v", err)
	}

	// 2. Extract — doc goes pending → classifying, tokens inserted.
	claimed, err := st.ClaimAndProcessDocument(ctx, extractFn)
	if err != nil {
		t.Fatalf("ClaimAndProcessDocument: %v", err)
	}
	if !claimed {
		t.Fatal("expected a document to be claimed")
	}

	doc, _, _ := st.GetDocument(ctx, "doc-cycle")
	if doc.Status != model.StatusClassifying {
		t.Errorf("after extraction: status = %q, want classifying", doc.Status)
	}
	if doc.TotalTokens == 0 {
		t.Error("after extraction: total_tokens = 0, expected > 0")
	}
	if doc.ExtractionStartedAt == nil || doc.ExtractionCompletedAt == nil {
		t.Error("after extraction: extraction timestamps not set")
	}

	// 3. Classify all tokens — doc goes classifying → completed.
	classifyAll(t, st)

	doc, _, _ = st.GetDocument(ctx, "doc-cycle")
	if doc.Status != model.StatusCompleted {
		t.Errorf("after classification: status = %q, want completed", doc.Status)
	}
	if doc.ClassifiedCount != doc.TotalTokens {
		t.Errorf("classified_count (%d) != total_tokens (%d)", doc.ClassifiedCount, doc.TotalTokens)
	}
	if doc.ClassificationCompletedAt == nil {
		t.Error("classification_completed_at not set on completed document")
	}

	// 4. All tokens must be classified.
	tokens, err := st.ListTokens(ctx, "doc-cycle", store.TokenFilter{Limit: 200})
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	for _, tok := range tokens {
		if tok.Status != model.TokenClassified {
			t.Errorf("token %d (%q): status = %q, want classified", tok.ID, tok.Text, tok.Status)
		}
		if tok.Classification == nil {
			t.Errorf("token %d (%q): Classification is nil after completion", tok.ID, tok.Text)
		}
	}
}

// ---- ListTokens filter tests -----------------------------------------------------------

func TestListTokens_Filters(t *testing.T) {
	truncate(t)
	st := newStore(t)
	ctx := context.Background()

	// Drive a document through the full pipeline.
	const text = "Acme Corporation hired Sarah Johnson on January 15, 2024."
	if _, _, err := st.CreateOrRerun(ctx, "doc-filter", text); err != nil {
		t.Fatalf("CreateOrRerun: %v", err)
	}
	if _, err := st.ClaimAndProcessDocument(ctx, extractFn); err != nil {
		t.Fatalf("ClaimAndProcessDocument: %v", err)
	}
	classifyAll(t, st)

	// Filter by classification=PERSON.
	people, err := st.ListTokens(ctx, "doc-filter", store.TokenFilter{
		Classification: string(model.CategoryPerson),
		Limit:          100,
	})
	if err != nil {
		t.Fatalf("ListTokens(PERSON): %v", err)
	}
	for _, tok := range people {
		if tok.Classification == nil || *tok.Classification != model.CategoryPerson {
			t.Errorf("token %q: wrong classification %v", tok.Text, tok.Classification)
		}
	}

	// Filter by status=classified (all tokens should be classified at this point).
	classified, err := st.ListTokens(ctx, "doc-filter", store.TokenFilter{
		Status: string(model.TokenClassified),
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("ListTokens(status=classified): %v", err)
	}
	all, _ := st.ListTokens(ctx, "doc-filter", store.TokenFilter{Limit: 100})
	if len(classified) != len(all) {
		t.Errorf("status=classified returned %d tokens, expected all %d", len(classified), len(all))
	}

	// Paging: limit=1 offset=0 and limit=1 offset=1 should give different tokens.
	page1, _ := st.ListTokens(ctx, "doc-filter", store.TokenFilter{Limit: 1, Offset: 0})
	page2, _ := st.ListTokens(ctx, "doc-filter", store.TokenFilter{Limit: 1, Offset: 1})
	if len(page1) > 0 && len(page2) > 0 && page1[0].ID == page2[0].ID {
		t.Error("paging returned the same token for offset 0 and offset 1")
	}
}
