package nlp

import (
	"context"
	"os"
	"testing"
)

func TestMockExtractor_Extract_ReturnsEntitiesInDocumentOrder(t *testing.T) {
	m := NewMockExtractor()
	// small.txt content — ~8 entities per README
	text := `Acme Corporation announced on January 15, 2024 that Sarah Johnson will join as Chief
Technology Officer. The company is headquartered at 500 Market Street in San Francisco.
Michael Chen, the outgoing officer, will advise Globex Industries starting March 1, 2024.
Investors including Bridgewater Partners welcomed the news.`

	entities, err := m.Extract(context.Background(), text)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if len(entities) == 0 {
		t.Fatal("expected at least one entity, got none")
	}

	// Entities must be ordered by CharOffset (document order).
	for i := 1; i < len(entities); i++ {
		if entities[i].CharOffset < entities[i-1].CharOffset {
			t.Errorf("entities out of order at index %d: offset %d < %d",
				i, entities[i].CharOffset, entities[i-1].CharOffset)
		}
	}
}

func TestMockExtractor_Extract_EntityFields(t *testing.T) {
	m := NewMockExtractor()
	entities, err := m.Extract(context.Background(), "Acme Corporation hired Sarah Johnson on January 15, 2024.")
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	for i, e := range entities {
		if e.Text == "" {
			t.Errorf("entity %d has empty Text", i)
		}
		if e.Type == "" {
			t.Errorf("entity %d has empty Type", i)
		}
		if e.CharOffset < 0 {
			t.Errorf("entity %d has negative CharOffset: %d", i, e.CharOffset)
		}
	}
}

func TestMockExtractor_Extract_NoOverlappingSpans(t *testing.T) {
	m := NewMockExtractor()
	text := "Acme Corporation hired Sarah Johnson on January 15, 2024 at 500 Market Street."
	entities, err := m.Extract(context.Background(), text)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	// No entity should start inside another entity's span.
	for i, a := range entities {
		endA := a.CharOffset + len(a.Text)
		for j, b := range entities {
			if i == j {
				continue
			}
			if b.CharOffset > a.CharOffset && b.CharOffset < endA {
				t.Errorf("entity %q overlaps with %q", a.Text, b.Text)
			}
		}
	}
}

func TestMockExtractor_Extract_EmptyText(t *testing.T) {
	m := NewMockExtractor()
	entities, err := m.Extract(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error on empty text: %v", err)
	}
	if len(entities) != 0 {
		t.Errorf("expected 0 entities for empty text, got %d", len(entities))
	}
}

func TestMockExtractor_Extract_LargeFile_AtLeast100Entities(t *testing.T) {
	data, err := os.ReadFile("../../testdata/large.txt")
	if err != nil {
		t.Fatalf("read large.txt: %v", err)
	}
	m := NewMockExtractor()
	entities, err := m.Extract(context.Background(), string(data))
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	const minEntities = 100
	if len(entities) < minEntities {
		t.Errorf("large.txt: got %d entities, want >= %d", len(entities), minEntities)
	}
}
