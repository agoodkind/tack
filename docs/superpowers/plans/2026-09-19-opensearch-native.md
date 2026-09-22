# Native OpenSearch Sparse Indexing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one production-connected official client that provisions and verifies complete native sparse semantic indexing.

**Architecture:** The official client owns transport behavior. Registered operator commands use the same adapter to provision and verify the cluster. A native `semantic` field stores the original page, creates overlapping character chunks, and runs the pinned sparse model. One strict generic access object supports partial permission updates without changing the semantic field. Tack validates bytes and document identity but never loads the model tokenizer.

**Tech Stack:** OpenSearch 3.8.0, `opensearch-go/v4` v4.7.3, ML Commons, Docker SDK, Go.

**Spec:** [Native sparse semantic indexing](../specs/2026-09-19-search-design.md#native-sparse-semantic-indexing).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Perform this Luna task only inside the selected Tack worktree. The only permitted external writes are the documented branch and pull-request publication operations. Use the exact image and model. Do not start OpenSearch or invoke Docker during this coding task. Do not add an ingest pipeline, tokenizer dependency, custom transport, OpenSearch plugin, fork, or external inference service.

## Review Focus

Test 4,096-byte Unicode, newlines, empty text, missing text, strict mappings,
access-only partial updates with the model unavailable, retired pages, endpoint
selection, model mismatch, and the 4 GiB memory failure.

---

### Task 1: Implement and register native sparse indexing control

**Files:**

- Create: `internal/adapters/search/opensearch.go`
- Create: `internal/adapters/search/opensearch_model.go`
- Create: `internal/adapters/search/opensearch_mapping.go`
- Create: `internal/testenv/opensearch.go`
- Create: `internal/testenv/opensearch_tls.go`
- Create: `internal/ops/cli_search.go`
- Create: `internal/ops/search_provision.go`
- Create: `internal/ops/search_verify.go`
- Create: `internal/config/search.go`
- Modify: `internal/config/config.go`
- Test: `internal/test/integration/search_native_test.go`
- Test: `internal/test/integration/search_control_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- This plan requires one validated stable endpoint, TLS CA bytes, credentials, and caller-selected shard counts.
- This plan implements the production `Adapter`, pinned `ModelInfo`, index creation, replica settings, and registered provision and verify operations.

```go
type Adapter struct { client *opensearchapi.Client }
type ModelInfo struct {
    ID, BundleSHA256, TokenizerSHA256, Algorithm string
    BundleBytes, RuntimeBytes int64
}
type IndexSpec struct { Model ModelInfo; MappingVersion string; Primaries, RoutingShards, Replicas int }
type IndexInfo struct { MappingVersion, ModelID string }
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
    adapter, client := newOpenSearchClients(t, "opensearchproject/opensearch:3.8.0", 8<<30)
    model, err := adapter.Provision(t.Context())
    if err != nil { t.Fatal(err) }
    index := createNativeSearchIndex(t, adapter, model, "search-v1", 1, 8, 0)
    putNativePage(t, client, index, unicodePage4096())
    stored := getNativePage(t, client, index)
    requireCompleteNativeChunks(t, stored, unicodePage4096())
}
```

- [ ] **Step 2: Record the deferred failure contract.**

The final validation plan runs `^TestSearchNativeSparse$` against the completed branch. The test must fail when the adapter, pinned model checks, native semantic mapping, or complete chunk coverage is absent. Do not start OpenSearch during this coding task.

- [ ] **Step 3: Add the official client and real TLS fixture.**

Pin v4.7.3. The existing Docker SDK test fixture launches the exact image during Sol validation. Create an ephemeral CA and server certificate. Keep credentials in memory. Configure `opensearch.Config` with one endpoint, CA bytes, request timeout, retry statuses, retry count, timeout retries, metrics, and a connection observer. Do not construct another `http.Client`. Do not invoke the fixture during this coding task.

- [ ] **Step 4: Prove every required client operation.**

In the real-engine compatibility test, construct the official client from the same production `opensearch.Config`. Execute typed index create, settings, split, bulk index, bulk partial update, point-in-time create and delete, alias, document get, health, block, statistics, refresh, and delete calls. Prove that bulk update accepts `version` with `version_type:external_gte` against OpenSearch 3.8.0. Build search requests with `SearchReq.GetRequest`. Use narrow `opensearch.Request` types plus `opensearch.Do` and `opensearch.ParseError` only for ML Commons and response fields absent from stable typed APIs. Add production adapter methods only for provision and verify operations in this task. Later slices add each operation when their production entry point uses it.

- [ ] **Step 5: Provision and verify the pinned model.**

Require model name `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte`, version `1.0.0`, bundle SHA-256 `08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047`, tokenizer SHA-256 `ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`, bundle size 554,924,400 bytes, sparse algorithm, deployed state, and eligible worker placement. Return an operator error for any mismatch.

- [ ] **Step 6: Create the strict native mapping.**

Require a nonempty mapping version, positive primary and routing-shard counts, a
nonnegative replica count, and a routing count divisible by every approved split
target. Store the mapping version and model ID in mapping `_meta`. Map identity,
revision, node type, and text projection fields as keywords, `name` as text,
`retired` as Boolean, and `search_generation` as a long. Add a strict `access`
object with keyword arrays `versions` and `keys` plus long `generation`. Search
code cannot add a field for a new permission type. Use `dynamic:strict` and this
field:

```json
{"page_text":{"type":"semantic","raw_field_type":"text","model_id":"registered-document-model-id","semantic_info_field_name":"page_text_semantic_info","chunking":[{"algorithm":"fixed_char_length","parameters":{"char_limit":160,"overlap_rate":0.5,"max_chunk_limit":-1}}],"sparse_encoding_config":{"prune_type":"max_ratio","prune_ratio":0.1},"skip_existing_embedding":true}}
```

- [ ] **Step 7: Add native chunk and source coverage.**

Index ordinary, Unicode, newline-only, empty, missing, retired, and unknown-field documents through typed bulk. Require the complete 4,096-byte source and final character, valid generated chunk text, forward progress, and finite sparse weights. Require strict mapping errors for unknown fields. Reject a missing required `page_text`. Accept active empty text without inference.

- [ ] **Step 8: Prove access-only updates preserve embeddings.**

Index one semantic page with `access.versions:["org-scope-v1"]`, one opaque
`access.keys` value, and generation 1. Capture its complete source, generated
chunks, and sparse weights. Undeploy the model. Submit a bulk partial update with
generation 2 and `version_type:external_gte` that replaces only `search_generation` and `access`.
Require success, changed access values, and byte-identical text, chunks, and
weights. Submit generation 1 again and require a version conflict. Submit
generation 2 again and require an idempotent result. This test must fail if the
update invokes model inference.

```json
{"update":{"_index":"node-pages-test","_id":"page-id","version":2,"version_type":"external_gte"}}
{"doc":{"search_generation":2,"access":{"versions":["permission-v2"],"keys":["permission-v2:opaque"],"generation":2}},"detect_noop":true}
```

- [ ] **Step 9: Add retirement and byte-bound coverage.**

Replace a same-ID document with `{"retired":true}` at the retirement version. Require no text or semantic fields and no active match. Reject page text above 4,096 UTF-8 bytes before an engine request.

- [ ] **Step 10: Add endpoint and resource coverage.**

Require `ConnectionObserver` to record only the configured stable endpoint. Final-validation measurements cover bundle size, reported inference memory, process memory, peak ingest memory, and latency in an 8 GiB container. Preserve the 4 GiB circuit-breaker failure as a regression. Sol records those measurements. Do not run either container during this coding task or infer concurrent capacity from this test.

- [ ] **Step 11: Register audited provision and verification commands.**

Add `ops search provision` and `ops search verify` through the existing `clispec` execute gate, result sink, and audit path. Both commands construct the production adapter from validated endpoint, CA, and credential configuration. `provision` registers and deploys the pinned model and creates the empty physical index and alias. `verify` checks the exact image-compatible model identity, mapping version, shard and routing-shard counts, replicas, alias target, health, and eligible inference placement. Neither command indexes Tack nodes or activates public search.

- [ ] **Step 12: Prove the production entry point.**

Add integration coverage that invokes both registered commands against the real TLS fixture during final validation. Require provisioning to be idempotent. Change each expected model, mapping, alias, and topology value independently and require verification to fail with one concrete mismatch. This command registration makes every new adapter and configuration declaration reachable in this task.

- [ ] **Step 13: Run the serial coding checks.**

Run: `make build`

Expected: PASS. The final validation plan runs the real OpenSearch checks.

- [ ] **Step 14: Create the bottom Graphite slice.**

```sh
git add go.mod go.sum internal/adapters/search internal/testenv/opensearch.go internal/testenv/opensearch_tls.go internal/ops/cli_search.go internal/ops/search_provision.go internal/ops/search_verify.go internal/config/search.go internal/config/config.go internal/test/integration/search_native_test.go internal/test/integration/search_control_test.go
```

Run Graphite MCP `create` with this exact message:

```text
Add native OpenSearch control operations

Co-authored-by: Codex <noreply@openai.com>
```

This branch is stack position 1 and starts from the released Meilisearch-removal `main`.
