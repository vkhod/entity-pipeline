package api

import (
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
