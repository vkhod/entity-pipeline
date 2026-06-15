package llm

import (
	"context"
	"testing"

	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
)

func TestCategoryFor(t *testing.T) {
	cases := []struct {
		nlpType model.EntityType
		want    model.Category
	}{
		{"PERSON", model.CategoryPerson},
		{"ORG", model.CategoryCompany},
		{"ADDRESS", model.CategoryAddress},
		{"GPE", model.CategoryAddress},
		{"LOC", model.CategoryAddress},
		{"DATE", model.CategoryDate},
		{"", model.CategoryUnknown},
		{"FOOBAR", model.CategoryUnknown},
	}
	for _, c := range cases {
		got := categoryFor(c.nlpType)
		if got != c.want {
			t.Errorf("categoryFor(%q) = %q, want %q", c.nlpType, got, c.want)
		}
	}
}

func TestMockClassifier_Classify_ResultsMatchInputOrder(t *testing.T) {
	c := NewMockClassifier(0) // no delay in tests
	tokens := []nlp.Entity{
		{Text: "Acme Corporation", Type: "ORG"},
		{Text: "Sarah Johnson", Type: "PERSON"},
		{Text: "January 15, 2024", Type: "DATE"},
		{Text: "500 Market Street", Type: "ADDRESS"},
	}
	want := []model.Category{
		model.CategoryCompany,
		model.CategoryPerson,
		model.CategoryDate,
		model.CategoryAddress,
	}

	results, err := c.Classify(context.Background(), tokens)
	if err != nil {
		t.Fatalf("Classify returned error: %v", err)
	}
	if len(results) != len(tokens) {
		t.Fatalf("expected %d results, got %d", len(tokens), len(results))
	}
	for i, r := range results {
		if r.Category != want[i] {
			t.Errorf("result[%d]: got category %q, want %q", i, r.Category, want[i])
		}
		if r.Confidence <= 0 || r.Confidence > 1 {
			t.Errorf("result[%d]: confidence %v out of range (0,1]", i, r.Confidence)
		}
		if r.Reasoning == "" {
			t.Errorf("result[%d]: empty reasoning", i)
		}
	}
}

func TestMockClassifier_Classify_EmptyBatch(t *testing.T) {
	c := NewMockClassifier(0)
	results, err := c.Classify(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error on nil batch: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for nil input, got %d", len(results))
	}
}
