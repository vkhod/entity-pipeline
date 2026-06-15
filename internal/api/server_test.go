package api

import (
	"net/url"
	"testing"
	"time"
)

func TestDurationMS_BothNil(t *testing.T) {
	if durationMS(nil, nil) != nil {
		t.Error("expected nil when both timestamps are nil")
	}
}

func TestDurationMS_StartNil(t *testing.T) {
	now := time.Now()
	if durationMS(nil, &now) != nil {
		t.Error("expected nil when start is nil")
	}
}

func TestDurationMS_EndNil(t *testing.T) {
	now := time.Now()
	if durationMS(&now, nil) != nil {
		t.Error("expected nil when end is nil")
	}
}

func TestDurationMS_Value(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(250 * time.Millisecond)
	got := durationMS(&start, &end)
	if got == nil {
		t.Fatal("expected non-nil duration")
	}
	if *got != 250 {
		t.Errorf("expected 250ms, got %dms", *got)
	}
}

func TestParseTokenFilter_Defaults(t *testing.T) {
	got, err := parseTokenFilter(url.Values{})
	if err != nil {
		t.Fatalf("parseTokenFilter returned unexpected error: %v", err)
	}
	if got.Limit != 100 {
		t.Errorf("Limit = %d, want 100", got.Limit)
	}
	if got.Offset != 0 {
		t.Errorf("Offset = %d, want 0", got.Offset)
	}
}

func TestParseTokenFilter_AcceptsValidValues(t *testing.T) {
	got, err := parseTokenFilter(url.Values{
		"classification": {"PERSON"},
		"status":         {"classified"},
		"limit":          {"25"},
		"offset":         {"50"},
		"page":           {"2"},
	})
	if err != nil {
		t.Fatalf("parseTokenFilter returned unexpected error: %v", err)
	}
	if got.Classification != "PERSON" || got.Status != "classified" {
		t.Errorf("unexpected filters: classification=%q status=%q", got.Classification, got.Status)
	}
	if got.Limit != 25 || got.Offset != 50 {
		t.Errorf("unexpected paging: limit=%d offset=%d", got.Limit, got.Offset)
	}
	if got.Page == nil || *got.Page != 2 {
		t.Errorf("Page = %v, want 2", got.Page)
	}
}

func TestParseTokenFilter_RejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		q    url.Values
	}{
		{name: "non-integer limit", q: url.Values{"limit": {"abc"}}},
		{name: "zero limit", q: url.Values{"limit": {"0"}}},
		{name: "large limit", q: url.Values{"limit": {"1001"}}},
		{name: "negative offset", q: url.Values{"offset": {"-1"}}},
		{name: "non-integer page", q: url.Values{"page": {"abc"}}},
		{name: "zero page", q: url.Values{"page": {"0"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTokenFilter(tc.q); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
