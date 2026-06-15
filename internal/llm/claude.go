package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/vkhod/entity-pipeline/internal/model"
	"github.com/vkhod/entity-pipeline/internal/nlp"
)

const (
	anthropicEndpoint = "https://api.anthropic.com/v1/messages"
	anthropicVersion  = "2023-06-01"
	maxRetryAttempts  = 3
)

// classifySystemPrompt is copied verbatim from PROMPTS.md §1.
const classifySystemPrompt = `You are an entity classifier in a document-processing pipeline. For each token you are given,
assign exactly one category:

- COMPANY  — a business, organization, institution, or brand (e.g. "Acme Corporation", "the Federal Reserve").
- PERSON   — a named individual (e.g. "Sarah Johnson").
- ADDRESS  — a physical location or address: street address, city, region, or country
             (e.g. "500 Market Street", "San Francisco").
- DATE     — a calendar date or clearly date-like expression (e.g. "January 15, 2024", "2024-03-01").
- UNKNOWN  — none of the above, or genuinely ambiguous from the text given.

Each token includes an ` + "`nlp_type`" + ` produced by an upstream extractor. Treat it as a hint, not
ground truth — the extractor is approximate and sometimes mislabels. Decide from the token text
itself.

Rules:
- Choose the single best category. Do not invent new categories.
- Use UNKNOWN when the text is ambiguous rather than guessing; reflect that in a lower confidence.
- ` + "`confidence`" + ` is your own calibrated certainty from 0.0 to 1.0.
- Keep ` + "`reasoning`" + ` to one short clause.

Return ONLY a JSON array, one object per input token, in the same order, with no surrounding
text or markdown:

[
  { "category": "COMPANY", "confidence": 0.97, "reasoning": "corporate suffix 'Corporation'" }
]`

// ClaudeClassifier is a real classifier backed by the Anthropic Messages API.
// Selected by CLASSIFIER=claude; defaults to claude-haiku-4-5-20251001.
type ClaudeClassifier struct {
	apiKey string
	model  string
	client *http.Client
}

func NewClaudeClassifier(apiKey, model string) *ClaudeClassifier {
	return &ClaudeClassifier{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	Temperature float64            `json:"temperature"`
	System      string             `json:"system"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type classifyItem struct {
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

func (c *ClaudeClassifier) Classify(ctx context.Context, tokens []nlp.Entity) ([]Result, error) {
	if len(tokens) == 0 {
		return nil, nil
	}

	var userMsg strings.Builder
	userMsg.WriteString("Classify these tokens:\n\n")
	for i, t := range tokens {
		fmt.Fprintf(&userMsg, "%d. text=%q   nlp_type=%s\n", i+1, t.Text, t.Type)
	}

	maxTokens := max(len(tokens)*80, 256)

	reqBytes, err := json.Marshal(anthropicRequest{
		Model:       c.model,
		Temperature: 0,
		System:      classifySystemPrompt,
		Messages:    []anthropicMessage{{Role: "user", Content: userMsg.String()}},
		MaxTokens:   maxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var respBytes []byte
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint, bytes.NewReader(reqBytes))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", anthropicVersion)
		req.Header.Set("content-type", "application/json")

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP request: %w", err)
		}
		respBytes, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response body: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			break
		}
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < maxRetryAttempts {
			base := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
			jitter := time.Duration(rand.Intn(100)) * time.Millisecond
			select {
			case <-time.After(base + jitter):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		return nil, fmt.Errorf("Anthropic API error %d: %s", resp.StatusCode, respBytes)
	}

	text, err := extractTextBlock(respBytes)
	if err != nil {
		return fallbackResults(len(tokens), fmt.Sprintf("parse API response: %v", err)), nil
	}

	results, err := parseClassificationText(text, len(tokens))
	if err != nil {
		return fallbackResults(len(tokens), fmt.Sprintf("parse classification array: %v", err)), nil
	}
	return results, nil
}

func extractTextBlock(body []byte) (string, error) {
	var resp anthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	for _, block := range resp.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text block in response")
}

func parseClassificationText(text string, n int) ([]Result, error) {
	// Strip optional ```json fences.
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if nl := strings.Index(text, "\n"); nl >= 0 {
			text = text[nl+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
		text = strings.TrimSpace(text)
	}

	// Extract outermost [...] even when the model adds surrounding prose.
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array in response: %.200s", text)
	}

	var items []classifyItem
	if err := json.Unmarshal([]byte(text[start:end+1]), &items); err != nil {
		return nil, fmt.Errorf("unmarshal array: %w", err)
	}

	results := make([]Result, n)
	for i := range results {
		if i < len(items) {
			results[i] = Result{
				Category:   parseCategory(items[i].Category),
				Confidence: clamp01(items[i].Confidence),
				Reasoning:  items[i].Reasoning,
			}
		} else {
			results[i] = Result{
				Category:   model.CategoryUnknown,
				Confidence: 0.1,
				Reasoning:  "fallback: model returned too few results",
			}
		}
	}
	return results, nil
}

func parseCategory(s string) model.Category {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "COMPANY":
		return model.CategoryCompany
	case "PERSON":
		return model.CategoryPerson
	case "ADDRESS":
		return model.CategoryAddress
	case "DATE":
		return model.CategoryDate
	default:
		return model.CategoryUnknown
	}
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func fallbackResults(n int, reason string) []Result {
	results := make([]Result, n)
	for i := range results {
		results[i] = Result{
			Category:   model.CategoryUnknown,
			Confidence: 0.1,
			Reasoning:  "fallback: " + reason,
		}
	}
	return results
}
