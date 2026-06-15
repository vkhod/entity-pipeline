// Package config loads runtime configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL     string
	HTTPAddr        string
	ClassifyBatch   int
	PollInterval    time.Duration
	Backoff         time.Duration
	ClassifierMode  string // mock | claude
	AnthropicAPIKey string
	AnthropicModel  string
	DemoDelay       time.Duration
}

func Load() Config {
	return Config{
		DatabaseURL:     env("DATABASE_URL", "postgres://pipeline:pipeline@localhost:5432/pipeline?sslmode=disable"),
		HTTPAddr:        env("HTTP_ADDR", ":8080"),
		ClassifyBatch:   envInt("CLASSIFY_BATCH_SIZE", 10),
		PollInterval:    envDur("POLL_INTERVAL", 500*time.Millisecond),
		Backoff:         envDur("RETRY_BACKOFF", 2*time.Second),
		ClassifierMode:  env("CLASSIFIER", "mock"),
		AnthropicAPIKey: env("ANTHROPIC_API_KEY", ""),
		AnthropicModel:  env("ANTHROPIC_MODEL", "claude-haiku-4-5-20251001"),
		DemoDelay:       envDur("CLASSIFY_DEMO_DELAY", 50*time.Millisecond),
	}
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if c.HTTPAddr == "" {
		return errors.New("HTTP_ADDR is required")
	}
	if c.ClassifyBatch < 1 {
		return fmt.Errorf("CLASSIFY_BATCH_SIZE must be >= 1, got %d", c.ClassifyBatch)
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("POLL_INTERVAL must be > 0, got %s", c.PollInterval)
	}
	if c.Backoff <= 0 {
		return fmt.Errorf("RETRY_BACKOFF must be > 0, got %s", c.Backoff)
	}
	if c.DemoDelay < 0 {
		return fmt.Errorf("CLASSIFY_DEMO_DELAY must be >= 0, got %s", c.DemoDelay)
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
