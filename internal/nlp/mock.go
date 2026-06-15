package nlp

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/vkhod/entity-pipeline/internal/model"
)

// MockExtractor is a deterministic, dependency-free stand-in for a real NER engine.
// It produces realistic-looking entities so the pipeline can be exercised end to end.
type MockExtractor struct{}

func NewMockExtractor() *MockExtractor { return &MockExtractor{} }

var (
	reDate    = regexp.MustCompile(`(?i)\b(?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|Aug(?:ust)?|Sep(?:tember)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)\s+\d{1,2},?\s+\d{4}\b|\b\d{4}-\d{2}-\d{2}\b|\b\d{1,2}/\d{1,2}/\d{2,4}\b`)
	reAddress = regexp.MustCompile(`\b\d{1,5}\s+(?:[A-Z][a-z]+\s){1,3}(?:Street|St|Avenue|Ave|Road|Rd|Boulevard|Blvd|Lane|Ln|Drive|Dr|Way|Court|Ct)\b\.?`)
	reProper  = regexp.MustCompile(`\b(?:[A-Z][A-Za-z&.]+\s){1,4}[A-Z][A-Za-z&.]+\b`)
	reOrgEnd  = regexp.MustCompile(`(?i)(?:Inc|Incorporated|Corp|Corporation|LLC|Ltd|Limited|Co|Company|Group|Holdings|Partners|Bank|Technologies|Systems|Industries)\.?$`)
)

type span struct {
	start, end int
	typ        model.EntityType
}

// Extract scans text and returns entities with positions, in document order.
// Overlapping matches are resolved by priority: dates and addresses before proper-noun runs.
func (m *MockExtractor) Extract(_ context.Context, text string) ([]Entity, error) {
	claimed := make([]bool, len(text)+1)
	var spans []span

	free := func(s, e int) bool {
		for i := s; i < e && i < len(claimed); i++ {
			if claimed[i] {
				return false
			}
		}
		return true
	}
	take := func(s, e int) {
		for i := s; i < e && i < len(claimed); i++ {
			claimed[i] = true
		}
	}

	for _, loc := range reDate.FindAllStringIndex(text, -1) {
		if free(loc[0], loc[1]) {
			take(loc[0], loc[1])
			spans = append(spans, span{loc[0], loc[1], "DATE"})
		}
	}
	for _, loc := range reAddress.FindAllStringIndex(text, -1) {
		if free(loc[0], loc[1]) {
			take(loc[0], loc[1])
			spans = append(spans, span{loc[0], loc[1], "ADDRESS"})
		}
	}
	for _, loc := range reProper.FindAllStringIndex(text, -1) {
		if !free(loc[0], loc[1]) {
			continue
		}
		frag := strings.TrimSpace(text[loc[0]:loc[1]])
		typ := model.EntityType("PERSON")
		if reOrgEnd.MatchString(frag) {
			typ = "ORG"
		}
		take(loc[0], loc[1])
		spans = append(spans, span{loc[0], loc[1], typ})
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	out := make([]Entity, 0, len(spans))
	for _, sp := range spans {
		out = append(out, Entity{
			Text:       strings.TrimSpace(text[sp.start:sp.end]),
			Type:       sp.typ,
			Page:       1,
			Sentence:   strings.Count(text[:sp.start], "."),
			CharOffset: sp.start,
		})
	}
	return out, nil
}
