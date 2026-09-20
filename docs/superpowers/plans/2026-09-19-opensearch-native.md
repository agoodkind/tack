# Native OpenSearch sparse indexing plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Index every reader page with complete native sparse semantic coverage.

**Architecture:** OpenSearch preserves the original page. Native ingest processors create bounded overlapping text, partition it at newlines, and generate nested sparse token weights.

**Tech Stack:** OpenSearch 3.8.0, ML Commons, Docker SDK, Go, validation-only Hugging Face Tokenizers 0.23.2.

**Spec:** [Native sparse semantic indexing](../specs/2026-09-19-search-design.md#native-sparse-semantic-indexing).

## Global constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints).
Tack's production packages have no tokenizer dependency. Search accepts successive
bounded pages until explicit completion. OpenSearch uses no custom plugin or fork.

## Verified configuration

Local OpenSearch 3.8.0 validated the exact configuration on 2026-09-19.

- Model: `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte`
  version 1.0.0.
- Bundle SHA-256:
  `08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047`.
- `ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`
  is the SHA-256 for the model's `tokenizer.json` file.
- Bundle size: 554,924,400 bytes. Reported inference memory: 665,909,280 bytes.
- Text inputs: at most 160 characters, starting every 80 characters.
- Model input limit: 512 tokens.

A 4,096-byte Unicode page retained its original text and complete generated text.
OpenSearch partitioned it into 243 inputs and generated 243 embeddings. Exhaustive
repeated-scalar validation found a maximum of 162 tokens. The tokenizer's Unicode
lowercase expansion was at most two scalars, so the 160-character bound remains
below 512 tokens.

The fixed 162-node relevance corpus ranked every target within the first 17 distinct
nodes. The lexical control found none. Three repeats preserved order. OpenSearch
accepted query weights from one native inference call and returned identical ranks
when later searches reused `query_tokens`.

The final GTE sparse run opened the ML memory circuit breaker at 4 GiB. The
equivalent 8 GiB run completed and used about 3.2 GiB afterward. Eight GiB sets the
QA floor and production starting allocation. Three-node QA still determines
production capacity.

## Task 1: Implement native sparse indexing and regression coverage

Create:

```text
internal/adapters/search/opensearch.go             verified TLS and bounded HTTP
internal/adapters/search/opensearch_model.go       pinned model provisioning
internal/adapters/search/opensearch_mapping.go     sparse pipeline and mapping
internal/testenv/opensearch.go                     real engine fixture
internal/testenv/opensearch_tls.go                 ephemeral certificates
internal/test/integration/search_native_test.go    public adapter coverage
internal/test/integration/search_tokenizer.py      isolated tokenizer proof
```

Launch the pinned image through the real test environment and Docker SDK. Keep test
credentials in memory. Do not modify deployed Compose in this task. Task 11 owns
the role-specific OpenSearch service.

Produce:

```go
type Config struct {
    URLs []string
    Username string
    Password string
    CAFile string
    Timeout time.Duration
}
type OpenSearchClient struct { config Config; http *http.Client }
type ModelInfo struct {
    ID, BundleSHA256, TokenizerSHA256, Algorithm string
    BundleBytes, RuntimeBytes int64
}
func NewOpenSearch(config Config) (*OpenSearchClient, error)
func (c *OpenSearchClient) JSON(context.Context, string, string, json.RawMessage) (json.RawMessage, error)
func (c *OpenSearchClient) Provision(context.Context) (ModelInfo, error)
func (c *OpenSearchClient) CreateIndex(context.Context, string, ModelInfo, int) error
```

- [ ] Add the public adapter regression test with a unique index and cleanup. Index
  ordinary, Unicode, newline-only, empty, missing, and retired pages through the real
  pipeline. Require strict mapping errors for unknown fields.
- [ ] Run `^TestSearchNativeSparse$` and record the missing-adapter failure.
- [ ] Implement `JSON` with verified TLS, round-robin starting-address selection,
  timeout, response-size limit, body closure, and typed non-2xx errors. Never log
  credentials or headers. The query task owns search retries through another address.
- [ ] Provision the pinned model. Reuse a registration only after checking its name,
  version, algorithm, size, bundle hash, tokenizer hash, deployment state, and worker
  placement. A mismatch requires an explicit operator error.
- [ ] Create this versioned ingest pipeline with typed Go request fields:

```json
{
  "processors": [
    {"gsub": {
      "field": "page_text",
      "target_field": "sparse_text",
      "pattern": "(?s)(.{1,80})(?=(.{0,80}))",
      "replacement": "$1$2\n",
      "if": "ctx.retired != true && ctx.page_text != ''"
    }},
    {"text_chunking": {
      "field_map": {"sparse_text": "sparse_chunks"},
      "algorithm": {"delimiter": {"delimiter": "\n", "max_chunk_limit": -1}},
      "ignore_missing": true
    }},
    {"sparse_encoding": {
      "model_id": "registered-model-id",
      "prune_type": "max_ratio",
      "prune_ratio": 0.1,
      "field_map": {"sparse_chunks": "sparse_embedding"},
      "skip_existing": false
    }}
  ]
}
```

- [ ] Create the index with a caller-supplied positive primary-shard count, one
  replica in deployment, and zero replicas in the one-node fixture. Use `dynamic:
  strict`. Map identity and revision fields as keywords, `page_text` and `name` as
  text, `sparse_text` and `sparse_chunks` as unindexed text, `retired` as Boolean,
  and `sparse_embedding` as nested with
  `sparse_encoding` mapped as `rank_features`. Do not enable `index.knn`.
- [ ] Extract the tokenizer from the exact registered bundle inside validation
  tooling. Disable truncation and padding. Verify the tokenizer configuration,
  checksums, 512-token limit, and every captured model input.
- [ ] Index a valid 4,096-byte page containing emoji, combining marks, CJK, Greek,
  Markdown, URLs, punctuation, long words, and newlines. Compare the original text,
  generated text, chunk partition, sparse output count, and final character.
- [ ] Enumerate every valid Unicode scalar in the tokenizer validation container.
  Test 160 repeats and maximum lowercase expansion. Fail if the structural bound or
  any actual input can reach 513 tokens.
- [ ] Index 4,096 newlines. Require bounded forward progress and one sparse result per
  generated nonempty input. Never infer completion from a chunk count.
- [ ] Replace a same-ID document with `{"retired":true}` at the retirement version.
  Require no text or sparse fields and no active search match. Accept active empty
  text without inference. Reject a missing required `page_text`.
- [ ] Reject source pages over 4,096 UTF-8 bytes before OpenSearch. Accept complete
  queries through the query task's byte bound without loading the tokenizer in Tack.
- [ ] Record bundle size, reported runtime memory, process memory, peak ingest memory,
  and inference latency in an 8 GiB container. Preserve the 4 GiB circuit-breaker
  regression. Do not infer concurrent capacity from this test.
- [ ] Run native tests and `make check`. Commit with subject
  `Add native OpenSearch sparse indexing validation` through the signed procedure.

The query task proves sparse relevance, one-time query inference, inverted-index
execution, and complete continuation. The deployment task proves three-node capacity.
