-- entity-pipeline schema.
-- Loaded automatically by Postgres from /docker-entrypoint-initdb.d on first boot.
-- (For production you would manage this with golang-migrate / goose instead.)

CREATE TABLE IF NOT EXISTS documents (
    id                           TEXT PRIMARY KEY,                  -- client-supplied document_id
    status                       TEXT NOT NULL DEFAULT 'pending',   -- pending|classifying|completed|failed
    generation                  INT  NOT NULL DEFAULT 1,            -- bumped on each full rerun (lineage)
    source_text                  TEXT NOT NULL,
    total_tokens                 INT  NOT NULL DEFAULT 0,
    classified_count             INT  NOT NULL DEFAULT 0,
    extraction_started_at        TIMESTAMPTZ,
    extraction_completed_at      TIMESTAMPTZ,
    classification_started_at    TIMESTAMPTZ,
    classification_completed_at  TIMESTAMPTZ,
    error                        TEXT,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT documents_status_chk
        CHECK (status IN ('pending','classifying','completed','failed'))
);

CREATE TABLE IF NOT EXISTS tokens (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id    TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    ordinal        INT  NOT NULL,                      -- extraction sequence within the document
    text           TEXT NOT NULL,
    nlp_type       TEXT NOT NULL,                      -- PERSON|ORG|GPE|DATE|ADDRESS|...
    page           INT  NOT NULL DEFAULT 1,
    sentence       INT  NOT NULL DEFAULT 0,
    char_offset    INT  NOT NULL DEFAULT 0,
    classification TEXT,                               -- COMPANY|PERSON|ADDRESS|DATE|UNKNOWN (null until classified)
    confidence     DOUBLE PRECISION,
    reasoning      TEXT,
    status         TEXT NOT NULL DEFAULT 'extracted',  -- extracted|classified
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    classified_at  TIMESTAMPTZ,
    CONSTRAINT tokens_doc_ordinal_uniq UNIQUE (document_id, ordinal),
    CONSTRAINT tokens_status_chk CHECK (status IN ('extracted','classified'))
);

-- Read-path support (1.2): query tokens by document, by classification, by page.
CREATE INDEX IF NOT EXISTS tokens_document_idx        ON tokens (document_id);
CREATE INDEX IF NOT EXISTS tokens_doc_classification  ON tokens (document_id, classification);
CREATE INDEX IF NOT EXISTS tokens_doc_page_idx        ON tokens (document_id, page);

-- Queue support: cheap claim of unfinished classification work (partial index on the hot predicate).
CREATE INDEX IF NOT EXISTS tokens_unclassified_idx
    ON tokens (document_id, ordinal) WHERE status = 'extracted';

-- Queue support: cheap claim of pending documents for extraction.
CREATE INDEX IF NOT EXISTS documents_pending_idx
    ON documents (created_at) WHERE status = 'pending';
