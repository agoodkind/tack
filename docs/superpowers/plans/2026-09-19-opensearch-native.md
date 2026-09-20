# Native Embedding Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Verify the unchanged OpenSearch model and splitter before depending on them.

**Architecture:** Start the pinned engine locally and index real text through a semantic field. Inspect its generated text segments and validate them with the tokenizer from the exact deployed bundle in a separate test container.

**Tech Stack:** OpenSearch 3.8.0, Docker SDK, Go, validation-only Hugging Face Tokenizers.

**Spec:** [Embedding requirements](../specs/2026-09-19-search-design.md#opensearch-text-splitting-and-embeddings).

## Validation result

Stop dependent implementation. Local semantic-field testing on 2026-09-19
found silent truncation in the proposed configuration. Keep the requirements unchanged until a
replacement configuration passes them.

OpenSearch 3.8.0 ran with the actual 1.0.2 TorchScript model in a disposable
4 GiB container with a 2 GiB heap. The model bundle truncates at 128 tokens.
Direct inference returned identical vectors for 126 repetitions of `alpha`
with and without a final `omega`. Replacing repetition 126 with `omega`
changed the vector. Repeating the control produced an identical vector.

| Configuration | Observed result |
| --- | --- |
| Proposed `fixed_token_length`, limit 384, overlap 0.2, unlimited chunks | Indexing succeeded with a segment requiring 2,306 model tokens. Punctuation-only and whitespace-only inputs failed with `empty docs`. |
| Native `fixed_char_length`, limit 32, overlap 0.2, unlimited chunks | The repeat corpus produced at most 95 model tokens per decoded segment and 647 segments in one document. An emoji fixture produced unpaired surrogate escapes that strict Unicode decoding rejected. Overlap preserved intact copies elsewhere; this does not prove emoji omission. |
| Diagnostic 96-byte Unicode-safe reader pages, native chunking disabled | All 13 inputs were reconstructed exactly from indexed pages with finite vectors. No page exceeded 98 model tokens. This alternative requires multiple production pages immediately and has not been accepted. |

These tests exercised actual engine indexing and inference. They did not verify
Tack integration, retrieval quality with small pages, or deployment capacity.
The mapping below remains the rejected candidate, not an approved default.

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). The model is `huggingface/sentence-transformers/all-MiniLM-L6-v2` version 1.0.2. No tokenizer dependency enters Tack's production indexing or query packages.

## Review Focus

Exercise long uninterrupted strings, combining characters, non-Latin text, more than 100 generated segments, and queries exceeding the actual model input length. None may succeed with silently truncated text.

---

## Task 1: Establish native coverage and the local engine fixture

Files to create:

```text
internal/adapters/search/opensearch.go             verified TLS and bounded HTTP
internal/adapters/search/opensearch_model.go       registration and deployment
internal/adapters/search/opensearch_mapping.go     semantic mapping and pipeline
internal/testenv/opensearch.go                    real managed engine fixture
internal/testenv/opensearch_tls.go                ephemeral test certificates
internal/test/integration/search_native_test.go   public adapter integration test
internal/test/integration/search_tokenizer.py     isolated coverage validation
```

Modify [Compose](../../../docker-compose.yml) to declare the pinned service image.
Use the existing testenv engine lifecycle and Docker SDK. Keep test credentials
in memory; use a test CA with valid SANs and verify it in the client.

Interfaces produced in the search adapter:

```go
type Config struct {
    URLs []string
    Username string
    Password string
    CAFile string
    Timeout time.Duration
}
type OpenSearchClient struct { config Config; http *http.Client }
type ModelInfo struct { ID, BundleSHA256, TokenizerSHA256 string; Dimensions int }
func NewOpenSearch(config Config) (*OpenSearchClient, error)
func (c *OpenSearchClient) JSON(ctx context.Context, method, path string, body json.RawMessage) (json.RawMessage, error)
func (c *OpenSearchClient) Provision(ctx context.Context) (ModelInfo, error)
func (c *OpenSearchClient) CreateIndex(ctx context.Context, index string, model ModelInfo, chunkLimit int) error
```

`testenv.OpenSearch(t T) searchadapter.Config` returns the managed endpoint and
test CA. `Provision` registers the specified TorchScript model, polls task state,
deploys it, and verifies dimension 384. It reuses an existing matching registration
only after checking the artifact identity. Pin downloaded artifacts by SHA-256;
record the tokenizer and sequence-length configuration from that same bundle.

- [ ] Add the real adapter test. Use a unique index per test and delete only that index during cleanup.

```go
func TestSearchNativeSegments(t *testing.T) {
    ctx := t.Context()
    client, err := searchadapter.NewOpenSearch(testenv.OpenSearch(t))
    if err != nil { t.Fatal(err) }
    model, err := client.Provision(ctx)
    if err != nil { t.Fatal(err) }
    index := "coverage-" + uuid.NewString()
    if err := client.CreateIndex(ctx, index, model, 1); err != nil { t.Fatal(err) }
    t.Cleanup(func() { _, err := client.JSON(context.Background(), "DELETE", "/"+index, nil); if err != nil { t.Error(err) } })
    body, err := json.Marshal(map[string]string{"page_text": strings.Repeat("alpha ", 150) + "omega"})
    if err != nil { t.Fatal(err) }
    if _, err := client.JSON(ctx, "PUT", "/"+index+"/_doc/one?refresh=wait_for", body); err != nil { t.Fatal(err) }
    raw, err := client.JSON(ctx, "GET", "/"+index+"/_doc/one", nil)
    if err != nil { t.Fatal(err) }
    var response struct { Source struct { Info struct { Chunks []struct {
        Text string `json:"text"`; Embedding []float64 `json:"embedding"`
    } `json:"chunks"` } `json:"page_text_semantic_info"` } `json:"_source"` }
    if err := json.Unmarshal(raw, &response); err != nil { t.Fatal(err) }
    chunks := response.Source.Info.Chunks
    if len(chunks) <= 100 { t.Fatalf("only %d chunks", len(chunks)) }
    if !strings.Contains(chunks[len(chunks)-1].Text, "omega") { t.Fatal("missing tail") }
    for _, chunk := range chunks {
        if len(chunk.Embedding) != 384 { t.Fatal("wrong vector dimension") }
        for _, value := range chunk.Embedding { if math.IsNaN(value) || math.IsInf(value, 0) { t.Fatal("nonfinite vector") } }
    }
}
```

- [ ] Run `^TestSearchNativeSegments$` with the root plan's container command. Initially expect the new adapter APIs to be undefined.
- [ ] Implement `JSON` with verified TLS, endpoint failover for transport failures, request timeout, response-size limit, non-2xx errors, and response-body closure. Do not log authorization headers or secret values.
- [ ] Implement the semantic mapping with these exact native options. Substitute the returned model ID and the test's `chunkLimit` using typed Go request fields and `json.Marshal`.

```json
{
  "settings": {"index.knn": true, "number_of_shards": 3, "number_of_replicas": 1},
  "mappings": {"properties": {
    "page_text": {
      "type": "semantic", "raw_field_type": "text", "model_id": "registered-model-id",
      "dense_embedding_config": {"method": {"name": "hnsw", "engine": "lucene"}},
      "chunking": [{"algorithm": "fixed_token_length", "parameters": {
        "token_limit": 384, "tokenizer": "standard", "overlap_rate": 0.2, "max_chunk_limit": -1
      }}]
    }
  }}
}
```

The JSON shows native syntax, not a validated release configuration. The [3.8
splitter](https://github.com/opensearch-project/neural-search/blob/3.8/src/main/java/org/opensearch/neuralsearch/processor/chunker/FixedTokenLengthChunker.java)
uses a different tokenizer from the model. Read generated segments from
`_source.page_text_semantic_info.chunks[].text` after actual indexing. A standalone
ingest simulation does not exercise semantic-field processing.

Verify dimension 384 and cosine similarity in the deployed model configuration
and generated vector mapping. The [semantic field configuration](https://docs.opensearch.org/latest/mappings/supported-field-types/semantic/#dense-embedding-configuration)
derives dimension and similarity from the model; do not add unsupported top-level
dimension or space_type parameters to dense_embedding_config.

- [ ] Add the validation-only tokenizer check. Run it in a managed test container with the deployed bundle and captured segments mounted read-only. The input file includes the actual sequence limit established from the model configuration, including special tokens.

```python
from __future__ import annotations

import sys
from pathlib import Path

from pydantic import BaseModel, Field
from tokenizers import Tokenizer


class CoverageCase(BaseModel):
    id: str
    segments: list[str]


class CoverageManifest(BaseModel):
    tokenizer_path: str
    sequence_limit: int = Field(gt=0)
    cases: list[CoverageCase]


def main() -> None:
    manifest = CoverageManifest.model_validate_json(Path(sys.argv[1]).read_text())
    tokenizer = Tokenizer.from_file(manifest.tokenizer_path)
    tokenizer.no_truncation()
    for case in manifest.cases:
        for position, text in enumerate(case.segments):
            encoded = tokenizer.encode(text, add_special_tokens=True)
            if len(encoded.ids) > manifest.sequence_limit:
                raise SystemExit(f"{case.id}: segment {position} exceeds model input")


if __name__ == "__main__":
    main()
```

- [ ] Index the acceptance corpus at the proposed production page budget. Include repeated emoji, combining marks, CJK, Markdown, URLs, uninterrupted random letters, and long whitespace. Check full decoded text coverage and the final character before tokenizer validation. Check both document segments and the complete query input. Retain the model preprocessing settings and a direct inference comparison as evidence that the measured limit matches actual truncation.
- [ ] Verify queries just below and above the proven limit. Use a conservative validated UTF-8 byte bound for query rejection; Tack checks bytes, not tokens. Do not choose a bound from a guessed token-to-character ratio.
- [ ] Require zero truncation failures, bounded nested-object count, and measured inference memory at the production page budget. A failing case blocks configuration approval. Save the reproducer and stop dependent execution instead of claiming success from vector counts.
- [ ] Run the test again, run `make check`, and commit with subject `Add native OpenSearch embedding coverage validation`.

The later MCP tests repeat these checks after public node operations. This task
proves engine behavior and supplies the test fixture; it does not establish the
entire Tack acceptance contract.
