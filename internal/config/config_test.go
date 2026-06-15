package config

import (
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		DatabaseURL:    "postgres://pipeline:pipeline@localhost:5432/pipeline?sslmode=disable",
		HTTPAddr:       ":8080",
		ClassifyBatch:  10,
		PollInterval:   500 * time.Millisecond,
		Backoff:        2 * time.Second,
		ClassifierMode: "mock",
		DemoDelay:      50 * time.Millisecond,
	}
}

func TestValidate_AcceptsValidConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
}

func TestValidate_RejectsInvalidClassifyBatch(t *testing.T) {
	cfg := validConfig()
	cfg.ClassifyBatch = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "CLASSIFY_BATCH_SIZE") {
		t.Fatalf("expected CLASSIFY_BATCH_SIZE error, got %v", err)
	}
}

func TestValidate_RejectsNonPositiveDurations(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{
			name: "poll interval",
			edit: func(c *Config) { c.PollInterval = 0 },
			want: "POLL_INTERVAL",
		},
		{
			name: "backoff",
			edit: func(c *Config) { c.Backoff = 0 },
			want: "RETRY_BACKOFF",
		},
		{
			name: "demo delay",
			edit: func(c *Config) { c.DemoDelay = -time.Millisecond },
			want: "CLASSIFY_DEMO_DELAY",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.edit(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s error, got %v", tc.want, err)
			}
		})
	}
}
