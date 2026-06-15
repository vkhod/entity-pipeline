# Prompts

This project uses prompts in two places, documented here for transparency.

1. **Runtime prompt** — what the classification stage sends to the model to categorize tokens.
2. **Development prompts** — how the design and implementation were driven with an AI assistant.

---

## 1. Runtime classification prompt

The classification worker claims a batch of extracted tokens and asks the model to categorize
all of them in a single call (one request per batch — cheaper and fewer round-trips than one
call per token). This is the prompt that backs `internal/llm/claude.go`.

### System prompt

```
You are an entity classifier in a document-processing pipeline. For each token you are given,
assign exactly one category:

- COMPANY  — a business, organization, institution, or brand (e.g. "Acme Corporation", "the Federal Reserve").
- PERSON   — a named individual (e.g. "Sarah Johnson").
- ADDRESS  — a physical location or address: street address, city, region, or country
             (e.g. "500 Market Street", "San Francisco").
- DATE     — a calendar date or clearly date-like expression (e.g. "January 15, 2024", "2024-03-01").
- UNKNOWN  — none of the above, or genuinely ambiguous from the text given.

Each token includes an `nlp_type` produced by an upstream extractor. Treat it as a hint, not
ground truth — the extractor is approximate and sometimes mislabels. Decide from the token text
itself.

Rules:
- Choose the single best category. Do not invent new categories.
- Use UNKNOWN when the text is ambiguous rather than guessing; reflect that in a lower confidence.
- `confidence` is your own calibrated certainty from 0.0 to 1.0.
- Keep `reasoning` to one short clause.

Return ONLY a JSON array, one object per input token, in the same order, with no surrounding
text or markdown:

[
  { "category": "COMPANY", "confidence": 0.97, "reasoning": "corporate suffix 'Corporation'" }
]
```

### User message (per batch)

```
Classify these tokens:

1. text="Acme Corporation"   nlp_type=ORG
2. text="Sarah Johnson"       nlp_type=PERSON
3. text="500 Market Street"   nlp_type=ADDRESS
4. text="January 15, 2024"    nlp_type=DATE
5. text="Q3"                  nlp_type=PERSON
```

### Expected shape of the response

```json
[
  { "category": "COMPANY", "confidence": 0.98, "reasoning": "corporate suffix 'Corporation'" },
  { "category": "PERSON",  "confidence": 0.97, "reasoning": "given name + surname" },
  { "category": "ADDRESS", "confidence": 0.95, "reasoning": "street number + street name" },
  { "category": "DATE",    "confidence": 0.99, "reasoning": "explicit calendar date" },
  { "category": "UNKNOWN", "confidence": 0.55, "reasoning": "ambiguous fiscal-quarter token; nlp_type likely wrong" }
]
```

### Engineering notes

- **Determinism:** call with `temperature: 0` so the same token classifies consistently.
- **Batching:** all tokens in the claimed batch go in one request; keep batches modest
  (`CLASSIFY_BATCH_SIZE`) so latency stays bounded while the claim transaction is open.
- **Robust parsing:** if the array length doesn't match the input or JSON fails to parse, fall
  back to `UNKNOWN` for the affected tokens rather than failing the whole batch.
- **Resilience:** wrap the call in retry with exponential backoff for `429`/`5xx`.
- **Model:** `claude-haiku-4-5-20251001` by default — a short, well-specified classification task
  doesn't need a larger model, which keeps cost and latency down. Override with `ANTHROPIC_MODEL`.

---

## 2. Development prompts

The architecture was developed through iterative design conversation before any code was
written — each decision (Postgres-as-queue vs. a broker, atomic extraction vs. incremental
classification, the reject-if-in-flight rerun policy, the failure-recovery model) was reasoned
through with trade-offs named, then captured in `ARCHITECTURE.md`. Implementation followed the
locked design.

<!--
TODO: curate the handful of prompts that best show the design reasoning and the build process —
e.g. the prompt that worked through the queue-vs-broker trade-off, the one that settled the
extraction/classification asymmetry, and the prompts used to scaffold and wire the store layer.
Keep them tight and representative rather than a full transcript.
-->
