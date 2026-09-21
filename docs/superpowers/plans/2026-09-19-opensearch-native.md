# Native OpenSearch Sparse Indexing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Index every bounded reader page with complete native sparse semantic coverage.

**Architecture:** The official client owns transport behavior. A native `semantic` field stores the original page, creates overlapping character chunks, and runs the pinned sparse model. Tack validates bytes and document identity but never loads the model tokenizer.

**Tech Stack:** OpenSearch 3.8.0, `opensearch-go/v4` v4.7.3, ML Commons, Docker SDK, Go.

**Spec:** [Native sparse semantic indexing](../specs/2026-09-19-search-design.md#native-sparse-semantic-indexing).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Use the exact image and model. Do not add an ingest pipeline, tokenizer dependency, custom transport, OpenSearch plugin, fork, or external inference service.

## Review Focus

Test 4,096-byte Unicode, newlines, empty text, missing text, strict mappings, retired pages, endpoint selection, model mismatch, and the 4 GiB memory failure.

---

### Task 1: Implement native sparse indexing

**Files:**

- Create: `internal/adapters/search/opensearch.go`
- Create: `internal/adapters/search/opensearch_model.go`
- Create: `internal/adapters/search/opensearch_mapping.go`
- Create: `internal/testenv/opensearch.go`
- Create: `internal/testenv/opensearch_tls.go`
- Test: `internal/test/integration/search_native_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- Consumes: one validated stable endpoint, TLS CA bytes, credentials, and caller-selected shard counts.
- Produces: `Adapter`, `ModelInfo`, index creation, replica settings, and typed core operations for later tasks.

```go
type Adapter struct { client *opensearchapi.Client }
type ModelInfo struct {
    ID, BundleSHA256, TokenizerSHA256, Algorithm string
    BundleBytes, RuntimeBytes int64
}
type IndexSpec struct { Model ModelInfo; AccessVersion string; Primaries, RoutingShards, Replicas int }
type IndexInfo struct { AccessVersion string }
func New(opensearch.Config) (*Adapter, error)
func (a *Adapter) Close() error
func (a *Adapter) Provision(context.Context) (ModelInfo, error)
func (a *Adapter) CreateIndex(context.Context, string, IndexSpec) error
func (a *Adapter) IndexInfo(context.Context, string) (IndexInfo, error)
func (a *Adapter) SetReplicas(context.Context, string, int) error
```

- [ ] **Step 1: Add the failing real-engine compatibility test.**

```go
func TestSearchNativeSparse(t *testing.T) {
    adapter := newOpenSearchAdapter(t, "opensearchproject/opensearch:3.8.0", 8<<30)
    model, err := adapter.Provision(t.Context())
    if err != nil { t.Fatal(err) }
    index := createNativeSearchIndex(t, adapter, model, "org-scope-v1", 1, 8, 0)
    putNativePage(t, adapter, index, unicodePage4096())
    stored := getNativePage(t, adapter, index)
    requireCompleteNativeChunks(t, stored, unicodePage4096())
}
```

- [ ] **Step 2: Record the deferred failure contract.**

Task 13 runs `^TestSearchNativeSparse$` against the completed branch. The test must fail when the adapter, pinned model checks, native semantic mapping, or complete chunk coverage is absent. Do not start OpenSearch during this coding task.

- [ ] **Step 3: Add the official client and real TLS fixture.**

Pin v4.7.3. Launch the exact image through the existing Docker SDK test environment. Create an ephemeral CA and server certificate. Keep credentials in memory. Configure `opensearch.Config` with one endpoint, CA bytes, request timeout, retry statuses, retry count, timeout retries, metrics, and a connection observer. Do not construct another `http.Client`.

- [ ] **Step 4: Prove every required client operation.**

Through the production adapter, execute typed index create, settings, split, bulk, point-in-time create and delete, alias, document get, health, block, statistics, refresh, and delete calls. Build search requests with `SearchReq.GetRequest`. Use narrow `opensearch.Request` types plus `opensearch.Do` and `opensearch.ParseError` only for ML Commons and response fields absent from stable typed APIs.

- [ ] **Step 5: Provision and verify the pinned model.**

Require model name `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte`, version `1.0.0`, bundle SHA-256 `08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047`, tokenizer SHA-256 `ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`, bundle size 554,924,400 bytes, sparse algorithm, deployed state, and eligible worker placement. Return an operator error for any mismatch.

- [ ] **Step 6: Create the strict native mapping.**

Require a nonempty access version, positive primary and routing-shard counts, a nonnegative replica count, and a routing count divisible by every approved split target. Store the access version in mapping `_meta`. Map identity, revision, node type, and projection fields as keywords, `name` as text, and `retired` as Boolean. Add a strict `access` object with keyword fields `org_id` and `scope_ids`. Use `dynamic:strict` and this field:

```json
{"page_text":{"type":"semantic","raw_field_type":"text","model_id":"registered-document-model-id","semantic_info_field_name":"page_text_semantic_info","chunking":[{"algorithm":"fixed_char_length","parameters":{"char_limit":160,"overlap_rate":0.5,"max_chunk_limit":-1}}],"sparse_encoding_config":{"prune_type":"max_ratio","prune_ratio":0.1},"skip_existing_embedding":true}}
```

- [ ] **Step 7: Add native chunk and source coverage.**

Index ordinary, Unicode, newline-only, empty, missing, retired, and unknown-field documents through typed bulk. Require the complete 4,096-byte source and final character, valid generated chunk text, forward progress, and finite sparse weights. Require strict mapping errors for unknown fields. Reject a missing required `page_text`. Accept active empty text without inference.

- [ ] **Step 8: Add retirement and byte-bound coverage.**

Replace a same-ID document with `{"retired":true}` at the retirement version. Require no text or semantic fields and no active match. Reject page text above 4,096 UTF-8 bytes before an engine request.

- [ ] **Step 9: Add endpoint and resource coverage.**

Require `ConnectionObserver` to record only the configured stable endpoint. Record bundle size, reported inference memory, process memory, peak ingest memory, and latency in an 8 GiB container. Preserve the 4 GiB circuit-breaker failure as a regression. Do not infer concurrent capacity from this test.

- [ ] **Step 10: Run the serial coding checks.**

Run: `go test ./internal/test/integration -run '^$' -count=1`

Run: `make check`

Expected: PASS after compiling the integration package without executing its tests. Task 13 runs the real OpenSearch checks.

- [ ] **Step 11: Commit the task.**

```sh
git add go.mod go.sum internal/adapters/search/opensearch.go internal/adapters/search/opensearch_model.go internal/adapters/search/opensearch_mapping.go internal/testenv/opensearch.go internal/testenv/opensearch_tls.go internal/test/integration/search_native_test.go
git commit -S -m "Add native OpenSearch sparse indexing validation" -m "Co-authored-by: Codex <noreply@openai.com>"
```
