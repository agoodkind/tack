# Native OpenSearch Embedding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Index each reader part completely with the verified native OpenSearch configuration.

**Architecture:** OpenSearch preserves each original part for keyword search. Its ingest pipeline creates overlapping texts with native regular-expression replacement. Its semantic field splits those texts at newlines and embeds each segment locally.

**Tech Stack:** OpenSearch 3.8.0, Docker SDK, Go, validation-only Hugging Face Tokenizers 0.20.3.

**Spec:** [Embedding requirements](../specs/2026-09-19-search-design.md#opensearch-text-splitting-and-embeddings).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Use `huggingface/sentence-transformers/all-MiniLM-L6-v2` version 1.0.2. Tack's production packages have no tokenizer dependency. The storage model remains unchanged; search accepts successive bounded parts until explicit completion.

## Review Focus

Test uninterrupted Unicode, combining marks, punctuation, newline-only parts, and retired documents. Preserve the original text, produce valid finite vectors, and prevent retired documents from acquiring new searchable text.

## Verified configuration

On 2026-09-19, actual OpenSearch 3.8.0 indexing passed 12 original cases and eight additional cases with the configuration below. Stored original text and generated segments matched exactly. All vectors were finite; the largest generated segment required 119 tokens. Repeated controls produced identical vectors. A 4,096-byte newline-only part produced 8,377 segments.

The rejected word splitter produced a segment requiring 2,306 tokens against the model's actual 128-token limit. The rejected character splitter generated unpaired Unicode surrogates. Neither splitter is part of this implementation.

The deployed DJL 0.31.1 library uses [Tokenizers 0.20.3](https://github.com/deepjavalibrary/djl/blob/v0.31.1/gradle/libs.versions.toml). Exhaustive enumeration of all 1,112,064 valid Unicode scalars with the pinned tokenizer established at most three normalized non-whitespace scalars per input scalar and one per UTF-8 byte. WordPiece consumes at least one such scalar per content token; the template adds two special tokens. Thus a 40-scalar segment plus newline requires at most 122 tokens, and a 126-byte query requires at most 128 tokens.

A 4,096-byte reader part contains at most 4,096 scalars. The pipeline duplicates at most that many scalars and adds at most 205 newlines. Even newline-only content therefore produces at most 8,397 nested segments, below OpenSearch's 10,000-object limit. These bounds do not establish deployment throughput or memory capacity under concurrent load.

## Task 1: Implement native embedding and regression coverage

Create the client, model provisioning, and mapping implementations in these focused files:

- [opensearch.go](../../../internal/adapters/search/opensearch.go) implements verified TLS and bounded HTTP.
- [opensearch_model.go](../../../internal/adapters/search/opensearch_model.go) registers and deploys the pinned model.
- [opensearch_mapping.go](../../../internal/adapters/search/opensearch_mapping.go) creates the ingest pipeline and semantic mapping.
- [testenv OpenSearch](../../../internal/testenv/opensearch.go) manages the real engine fixture.
- [testenv TLS](../../../internal/testenv/opensearch_tls.go) creates ephemeral certificates.
- [native integration tests](../../../internal/test/integration/search_native_test.go) exercise public adapter calls.
- [tokenizer validation](../../../internal/test/integration/search_tokenizer.py) validates captured model inputs in an isolated test container.

Modify [Compose](../../../docker-compose.yml) to declare the pinned image. Use the existing testenv lifecycle and Docker SDK. Keep test credentials in memory; verify the test CA and certificate SANs in the client.

The adapter produces these interfaces:

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
func (c *OpenSearchClient) CreateIndex(ctx context.Context, index string, model ModelInfo) error
```

`testenv.OpenSearch(t T) searchadapter.Config` returns the endpoint and CA. `Provision` registers the TorchScript model, polls task state, deploys it, and verifies dimension 384 and distance `l2`. Reuse a registration only after verifying artifact identity. Pin the bundle SHA-256 to `25e2858993cd477936f24e412a508b005aa6b59a308301cc69690e4b90cab439` and tokenizer SHA-256 to `da0e79933b9ed51798a3ae27893d3c5fa4a201126cef75586296df9b4d2c62a0`.

- [ ] Add the public adapter regression test with a unique index and index-specific cleanup.

```go
func TestSearchNativeSegments(t *testing.T) {
    ctx := t.Context()
    client, err := searchadapter.NewOpenSearch(testenv.OpenSearch(t))
    if err != nil { t.Fatal(err) }
    model, err := client.Provision(ctx)
    if err != nil { t.Fatal(err) }
    index := "coverage-" + uuid.NewString()
    if err := client.CreateIndex(ctx, index, model); err != nil { t.Fatal(err) }
    t.Cleanup(func() { _, err := client.JSON(context.Background(), "DELETE", "/"+index, nil); if err != nil { t.Error(err) } })
    source := strings.Repeat("!", 4091) + "omega"
    body, err := json.Marshal(map[string]string{"page_text": source})
    if err != nil { t.Fatal(err) }
    if _, err := client.JSON(ctx, "PUT", "/"+index+"/_doc/one?refresh=wait_for", body); err != nil { t.Fatal(err) }
    raw, err := client.JSON(ctx, "GET", "/"+index+"/_doc/one", nil)
    if err != nil { t.Fatal(err) }
    var response struct { Source struct {
        PageText string `json:"page_text"`
        Info struct { Chunks []struct {
            Text string `json:"text"`; Embedding []float64 `json:"embedding"`
        } `json:"chunks"` } `json:"embedding_text_semantic_info"`
    } `json:"_source"` }
    if err := json.Unmarshal(raw, &response); err != nil { t.Fatal(err) }
    if response.Source.PageText != source { t.Fatal("changed original text") }
    chunks := response.Source.Info.Chunks
    if len(chunks) <= 100 || len(chunks) > 8397 { t.Fatalf("invalid chunk count %d", len(chunks)) }
    if !strings.Contains(chunks[len(chunks)-1].Text, "omega") { t.Fatal("missing tail") }
    for _, chunk := range chunks {
        if len(chunk.Embedding) != 384 { t.Fatal("wrong vector dimension") }
        for _, value := range chunk.Embedding { if math.IsNaN(value) || math.IsInf(value, 0) { t.Fatal("nonfinite vector") } }
    }
}
```

- [ ] Run `^TestSearchNativeSegments$` with the root plan's container command. Record the missing-adapter compilation failure before implementation.
- [ ] Implement `JSON` with verified TLS, transport-failure endpoint failover, timeout, response-size limit, non-2xx errors, and response-body closure. Exclude credentials and authorization headers from logs.
- [ ] Implement model provisioning and create this versioned ingest pipeline before the index. Use typed Go request fields and `json.Marshal`.

```json
{"processors":[{"gsub":{
  "field":"page_text", "target_field":"embedding_text",
  "pattern":"(?s)(.{1,20})(?=(.{0,20}))", "replacement":"$1$2\n",
  "if":"ctx.retired != true && ctx.page_text != ''"
}}]}
```

- [ ] Create the index with these fields and options. Substitute the registered model ID and versioned pipeline ID. Include the identity, scope, revision, and ordinal fields required by the spec. The local one-node fixture uses zero replicas; deployment uses one.

```json
{
  "settings": {"index.knn":true, "number_of_shards":3, "number_of_replicas":1,
    "default_pipeline":"node-pages-embedding-v1"},
  "mappings":{"properties":{
    "page_text":{"type":"text"},
    "retired":{"type":"boolean"},
    "embedding_text":{
      "type":"semantic", "model_id":"registered-model-id",
      "dense_embedding_config":{"method":{"name":"hnsw","engine":"lucene"}},
      "chunking":[{"algorithm":"delimiter","parameters":{
        "delimiter":"\n", "max_chunk_limit":-1
      }}]
    }
  }}
}
```

The original `page_text` supports keyword search. Read actual model inputs from `_source.embedding_text_semantic_info.chunks[].text` after indexing. The [semantic field configuration](https://docs.opensearch.org/latest/mappings/supported-field-types/semantic/#dense-embedding-configuration) derives dimension and distance from the deployed model; do not add unsupported top-level dimension or distance parameters. A standalone ingest simulation does not exercise semantic-field inference.

- [ ] Add the tokenizer check in the managed test container. Mount the exact deployed bundle and captured segments read-only; pin Tokenizers 0.20.3. Disable truncation and padding before counting all tokens, including special tokens.

```python
from __future__ import annotations

import sys
from pathlib import Path

from pydantic import BaseModel
from tokenizers import Tokenizer


class CoverageCase(BaseModel):
    id: str
    segments: list[str]


class CoverageManifest(BaseModel):
    tokenizer_path: str
    cases: list[CoverageCase]


def main() -> None:
    manifest = CoverageManifest.model_validate_json(Path(sys.argv[1]).read_text())
    tokenizer = Tokenizer.from_file(manifest.tokenizer_path)
    tokenizer.no_truncation()
    tokenizer.no_padding()
    for case in manifest.cases:
        for position, text in enumerate(case.segments):
            if len(tokenizer.encode(text, add_special_tokens=True).ids) > 128:
                raise SystemExit(f"{case.id}: segment {position} exceeds model input")


if __name__ == "__main__":
    main()
```

- [ ] Index the acceptance corpus with the production 4,096-byte part bound. Include repeated emoji, combining marks, CJK, decomposing Hangul, Markdown, URLs, uninterrupted letters, punctuation, and whitespace. Check exact original preservation, complete generated-text coverage, valid Unicode, finite vectors, and final-character coverage before tokenizer validation.
- [ ] Index 4,096 newlines and require 8,377 finite vectors. The later reader and indexing tasks enforce the 4,096-byte part bound without truncation and repeat coverage through public node reads.
- [ ] Replace an indexed document with a same-ID retirement document containing `retired:true` and no text fields. Require no `embedding_text` or semantic information in stored source, no keyword match, and no result from the active-document vector query. This exercises the ingest condition during cleanup.
- [ ] Index an active document with `page_text:""` and require success without embedding fields. Omit `page_text` from another active document and require HTTP 400. Empty content is valid; missing required content is an error.
- [ ] Validate complete queries of 126 UTF-8 bytes, including 126 exclamation marks requiring exactly 128 tokens. Verify that 127 exclamation marks require 129 tokens without truncation. The later query task rejects inputs over 126 bytes before inference; Tack checks bytes only.
- [ ] Repeat direct inference controls around the actual 128-token limit and retain exact model preprocessing settings. Capture peak inference memory at the production part bound for deployment sizing; do not infer concurrent capacity from a successful finite corpus.
- [ ] Run the native regression test, run `make check`, and commit with subject `Add native OpenSearch embedding coverage validation` using the root plan's signed commit procedure.

Later MCP tests repeat coverage through public node operations. This task implements the verified engine configuration and its fixture; the runtime and deployment tasks establish their own acceptance results.
