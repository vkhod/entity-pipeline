// Package model holds the core domain types shared across the pipeline.
package model

import "time"

// DocumentStatus is the lifecycle state of a document.
type DocumentStatus string

const (
	StatusPending     DocumentStatus = "pending"     // created, awaiting extraction
	StatusClassifying DocumentStatus = "classifying" // tokens extracted, classification in progress
	StatusCompleted   DocumentStatus = "completed"   // all tokens classified
	StatusFailed      DocumentStatus = "failed"      // unrecoverable error
)

// TokenStatus is the lifecycle state of a single token.
type TokenStatus string

const (
	TokenExtracted  TokenStatus = "extracted"  // produced by NLP, awaiting classification
	TokenClassified TokenStatus = "classified" // classified by the LLM stage
)

// EntityType is the NLP stage's notion of an entity (PERSON, ORG, GPE, DATE, ADDRESS, ...).
type EntityType string

// Category is the classification stage's output.
type Category string

const (
	CategoryCompany Category = "COMPANY"
	CategoryPerson  Category = "PERSON"
	CategoryAddress Category = "ADDRESS"
	CategoryDate    Category = "DATE"
	CategoryUnknown Category = "UNKNOWN"
)

// Document is the processing manifest — the single source of truth for a document's state.
type Document struct {
	ID                        string         `json:"document_id"`
	Status                    DocumentStatus `json:"status"`
	Generation                int            `json:"generation"`
	SourceText                string         `json:"-"`
	TotalTokens               int            `json:"total_tokens"`
	ClassifiedCount           int            `json:"classified_count"`
	ExtractionStartedAt       *time.Time     `json:"extraction_started_at,omitempty"`
	ExtractionCompletedAt     *time.Time     `json:"extraction_completed_at,omitempty"`
	ClassificationStartedAt   *time.Time     `json:"classification_started_at,omitempty"`
	ClassificationCompletedAt *time.Time     `json:"classification_completed_at,omitempty"`
	Error                     string         `json:"error,omitempty"`
	CreatedAt                 time.Time      `json:"created_at"`
	UpdatedAt                 time.Time      `json:"updated_at"`
}

// Token is an extracted (and possibly classified) entity occurrence.
type Token struct {
	ID             int64       `json:"id"`
	DocumentID     string      `json:"document_id"`
	Ordinal        int         `json:"ordinal"`
	Text           string      `json:"text"`
	NLPType        EntityType  `json:"nlp_type"`
	Page           int         `json:"page"`
	Sentence       int         `json:"sentence"`
	CharOffset     int         `json:"char_offset"`
	Classification *Category   `json:"classification,omitempty"`
	Confidence     *float64    `json:"confidence,omitempty"`
	Reasoning      string      `json:"reasoning,omitempty"`
	Status         TokenStatus `json:"status"`
	CreatedAt      time.Time   `json:"created_at"`
	ClassifiedAt   *time.Time  `json:"classified_at,omitempty"`
}
