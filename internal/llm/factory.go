package llm

import (
	"errors"
	"fmt"
	"time"
)

// NewClassifier selects an implementation from configuration:
//
//	mode "mock"   -> MockClassifier (default)
//	mode "claude" -> ClaudeClassifier (real, Anthropic Messages API)
func NewClassifier(mode, apiKey, anthropicModel string, demoDelay time.Duration) (Classifier, error) {
	switch mode {
	case "claude":
		if apiKey == "" {
			return nil, errors.New("ANTHROPIC_API_KEY is required when CLASSIFIER=claude")
		}
		return NewClaudeClassifier(apiKey, anthropicModel), nil
	case "mock", "":
		return NewMockClassifier(demoDelay), nil
	default:
		return nil, fmt.Errorf("unknown CLASSIFIER mode %q (want mock|claude)", mode)
	}
}
