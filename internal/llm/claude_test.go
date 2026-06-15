package llm

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
)

func TestParseClassificationText_ValidJSON(t *testing.T) {
	results, err := parseClassificationText(`[
		{"category":"COMPANY","confidence":0.98,"reasoning":"corporate suffix"},
		{"category":"person","confidence":1.2,"reasoning":"name"},
		{"category":"mystery","confidence":-0.5,"reasoning":"unknown label"}
	]`, 3)
	if err != nil {
		t.Fatalf("parseClassificationText returned error: %v", err)
	}

	if results[0].Category != model.CategoryCompany {
		t.Errorf("result[0].Category = %q, want COMPANY", results[0].Category)
	}
	if results[1].Category != model.CategoryPerson {
		t.Errorf("result[1].Category = %q, want PERSON", results[1].Category)
	}
	if results[1].Confidence != 1 {
		t.Errorf("result[1].Confidence = %v, want clamped 1", results[1].Confidence)
	}
	if results[2].Category != model.CategoryUnknown {
		t.Errorf("result[2].Category = %q, want UNKNOWN", results[2].Category)
	}
	if results[2].Confidence != 0 {
		t.Errorf("result[2].Confidence = %v, want clamped 0", results[2].Confidence)
	}
}

func TestParseClassificationText_FencedJSONWithProse(t *testing.T) {
	results, err := parseClassificationText("```json\nnoise before\n[{\"category\":\"DATE\",\"confidence\":0.9,\"reasoning\":\"calendar date\"}]\nnoise after\n```", 1)
	if err != nil {
		t.Fatalf("parseClassificationText returned error: %v", err)
	}

	if results[0].Category != model.CategoryDate {
		t.Errorf("Category = %q, want DATE", results[0].Category)
	}
	if results[0].Confidence != 0.9 {
		t.Errorf("Confidence = %v, want 0.9", results[0].Confidence)
	}
}

func TestParseClassificationText_TooFewResultsFallsBackPerMissingItem(t *testing.T) {
	results, err := parseClassificationText(`[{"category":"ADDRESS","confidence":0.8,"reasoning":"street"}]`, 2)
	if err != nil {
		t.Fatalf("parseClassificationText returned error: %v", err)
	}

	if results[0].Category != model.CategoryAddress {
		t.Errorf("result[0].Category = %q, want ADDRESS", results[0].Category)
	}
	if results[1].Category != model.CategoryUnknown {
		t.Errorf("result[1].Category = %q, want UNKNOWN fallback", results[1].Category)
	}
	if !strings.Contains(results[1].Reasoning, "too few results") {
		t.Errorf("result[1].Reasoning = %q, want too-few fallback", results[1].Reasoning)
	}
}

func TestParseClassificationText_MalformedResponseReturnsError(t *testing.T) {
	if _, err := parseClassificationText("not json", 1); err == nil {
		t.Fatal("expected error for response without JSON array")
	}
	if _, err := parseClassificationText(`[{"category":]`, 1); err == nil {
		t.Fatal("expected error for malformed JSON array")
	}
}

func TestExtractTextBlock(t *testing.T) {
	text, err := extractTextBlock([]byte(`{
		"content": [
			{"type": "tool_use", "text": "ignore"},
			{"type": "text", "text": "[{\"category\":\"PERSON\"}]"}
		]
	}`))
	if err != nil {
		t.Fatalf("extractTextBlock returned error: %v", err)
	}
	if text != `[{"category":"PERSON"}]` {
		t.Errorf("text = %q, want first text block", text)
	}
}

func TestFallbackResults(t *testing.T) {
	results := fallbackResults(2, "bad JSON")
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for i, r := range results {
		if r.Category != model.CategoryUnknown {
			t.Errorf("result[%d].Category = %q, want UNKNOWN", i, r.Category)
		}
		if r.Confidence != 0.1 {
			t.Errorf("result[%d].Confidence = %v, want 0.1", i, r.Confidence)
		}
		if !strings.Contains(r.Reasoning, "bad JSON") {
			t.Errorf("result[%d].Reasoning = %q, want reason included", i, r.Reasoning)
		}
	}
}

func TestClaudeClassifier_Classify_Integration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping Claude integration test")
	}

	modelName := os.Getenv("ANTHROPIC_MODEL")
	if modelName == "" {
		modelName = "claude-haiku-4-5-20251001"
	}

	c := NewClaudeClassifier(apiKey, modelName)
	tokens := []nlp.Entity{
		{Text: "Acme Corporation", Type: "ORG"},
		{Text: "Sarah Johnson", Type: "PERSON"},
		{Text: "January 15, 2024", Type: "DATE"},
	}

	results, err := c.Classify(context.Background(), tokens)
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	if len(results) != len(tokens) {
		t.Fatalf("expected %d results, got %d", len(tokens), len(results))
	}

	validCategories := map[model.Category]bool{
		model.CategoryCompany: true,
		model.CategoryPerson:  true,
		model.CategoryAddress: true,
		model.CategoryDate:    true,
		model.CategoryUnknown: true,
	}
	for i, r := range results {
		if !validCategories[r.Category] {
			t.Errorf("result[%d]: invalid category %q", i, r.Category)
		}
		if r.Confidence < 0 || r.Confidence > 1 {
			t.Errorf("result[%d]: confidence %v out of [0,1]", i, r.Confidence)
		}
	}
}
