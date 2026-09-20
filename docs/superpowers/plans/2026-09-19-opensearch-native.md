# Native OpenSearch sparse indexing plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Index every reader page with complete native sparse semantic coverage.

**Architecture:** OpenSearch preserves the original page. Its `semantic` field creates bounded overlapping text and sparse embeddings. The official Go client owns TLS, connection pooling, retries, and core API encoding. Deployment supplies one stable environment endpoint.

**Tech Stack:** OpenSearch 3.8.0, `github.com/opensearch-project/opensearch-go/v4` v4.7.3, ML Commons, Docker SDK, and Go.

**Spec:** [Native sparse semantic indexing](../specs/2026-09-19-search-design.md#native-sparse-semantic-indexing).

## Global constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Tack's production packages have no tokenizer dependency. Search accepts successive bounded pages until explicit completion. OpenSearch uses no custom plugin, fork, or application-defined ingest pipeline.

## Verified configuration

The [prototype and validation record](2026-09-19-opensearch-validation.md) preserves the passed, failed, and superseded experiments that selected this configuration. Local OpenSearch 3.8.0 validated the final field and client configuration on 2026-09-19. Native split validation followed on 2026-09-20.

- Client v4.7.3 completed index creation, typed bulk, refresh, point-in-time creation and deletion, typed search, atomic alias changes, index and document reads, index deletion, close, and transport metrics against the exact 3.8.0 image.
- Model `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0 has bundle SHA-256 `08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047`.
- The bundled `tokenizer.json` has SHA-256 `ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`. Tack records this artifact identity but does not run or reproduce its logic.
- The bundle uses 554,924,400 bytes. OpenSearch reports 665,909,280 bytes of inference memory.
- A 4,096-byte Unicode page retained its complete source and final character. The semantic field created 27 chunks and 27 sparse embeddings. The largest chunk contained 152 characters, and the largest start-position step contained 76 characters.
- One query inference produced weights that returned every relevance target within the first 18 distinct nodes. Lexical controls returned no target. Three repeats preserved order.
- Point-in-time traversal returned 37,500 page matches and 1,501 distinct node IDs in 375 batches of 100. One node contributed 36,000 pages. The traversal completed in 4.786 seconds after one query inference.
- A 4 GiB container opened the ML memory circuit breaker. The equivalent 8 GiB run completed and used about 3.4 GiB afterward. Eight GiB remains the floor for every guest that runs the model.
- On 2026-09-20, client v4.7.3's typed `Indices.Split` API split this exact semantic mapping from one primary shard to two. The undeployed model proved that existing documents were not inferred again. Mappings, five documents, seven chunks, sparse weights, and the saved raw-sparse query matched byte for byte. Repeated native splits completed from one to two, four, and eight primary shards. A combined lexical query kept its result order but changed numeric scores because primary shards use local term statistics.

## Task 1: Implement native sparse indexing and regression coverage

Create:

```text
internal/adapters/search/opensearch.go             official client construction and lifecycle
internal/adapters/search/opensearch_model.go       concrete ML Commons operations
internal/adapters/search/opensearch_mapping.go     native semantic mapping
internal/testenv/opensearch.go                     real engine fixture
internal/testenv/opensearch_tls.go                 ephemeral certificates
internal/test/integration/search_native_test.go    public adapter coverage
```

Launch the pinned image through the real test environment and Docker SDK. Keep test credentials in memory. Do not modify deployed Compose in this task. Task 11 owns the role-specific OpenSearch service.

Produce:

```go
type Adapter struct {
    client *opensearchapi.Client
}
type ModelInfo struct {
    ID, BundleSHA256, TokenizerSHA256, Algorithm string
    BundleBytes, RuntimeBytes int64
}
func New(config opensearch.Config) (*Adapter, error)
func (a *Adapter) Close() error
func (a *Adapter) Provision(context.Context) (ModelInfo, error)
func (a *Adapter) CreateIndex(ctx context.Context, index string, model ModelInfo, primaryShards, routingShards, replicas int) error
func (a *Adapter) SetReplicas(ctx context.Context, index string, replicas int) error
```

- [ ] Add v4.7.3 and run a compatibility test through the production adapter against `opensearchproject/opensearch:3.8.0`. Exercise every typed core API used by later tasks. Treat this gate as required because the v4 client documents later 3.x releases as best effort.
- [ ] Run `^TestSearchNativeSparse$` and record the missing-adapter failure.
- [ ] Convert the central application configuration to `opensearch.Config`. Configure one endpoint, credentials, CA bytes, request timeout, retry statuses, retry count, timeout retry policy, client metrics, error reporting for partial bulk and search failures, and lifecycle close. Do not construct another `http.Client`, retry loop, or backend selector.
- [ ] Use typed client APIs for index creation, settings, split, bulk, point-in-time, alias, document, health, block, and statistics operations. Build search requests with the typed API. Decode the narrow search response with `opensearch.Do`. Define narrow ML Commons request types that satisfy `opensearch.Request`, then call `opensearch.Do` and `opensearch.ParseError`. Stable v4.7.3 omits replacement PIT IDs from `SearchResp` and lacks ML Commons APIs. Do not expose a generic method-and-path JSON function or import the temporary v5 preview package.
- [ ] Provision the pinned model. Reuse a registration only after checking its name, version, algorithm, size, bundle hash, deployment state, and worker placement. A mismatch produces an operator error.
- [ ] Create the index with `opensearchapi.Indices.Create` and caller-supplied replica, positive primary, and positive routing-shard counts. Implement `SetReplicas` with the typed index settings API. QA, initial production, and the one-node fixture use zero replicas. Production changes the count to one only after additional data nodes join. Require the routing count to be divisible by the primary count and every approved split target. Store all three counts with the physical generation. Use `dynamic: strict` and this native field configuration:

```json
{
  "page_text": {
    "type": "semantic",
    "raw_field_type": "text",
    "model_id": "registered-document-model-id",
    "semantic_info_field_name": "page_text_semantic_info",
    "chunking": [{
      "algorithm": "fixed_char_length",
      "parameters": {
        "char_limit": 160,
        "overlap_rate": 0.5,
        "max_chunk_limit": -1
      }
    }],
    "sparse_encoding_config": {
      "prune_type": "max_ratio",
      "prune_ratio": 0.1
    },
    "skip_existing_embedding": true
  }
}
```

- [ ] Map identity and revision fields as keywords, `name` as text, and `retired` as Boolean. Verify that OpenSearch creates nested `page_text_semantic_info.chunks`, text at `.chunks.text`, and `rank_features` at `.chunks.embedding`. Do not create `sparse_text`, `sparse_chunks`, `sparse_embedding`, an ingest pipeline, or `index.knn`.
- [ ] Index ordinary, Unicode, newline-only, empty, missing, and retired pages through the typed bulk API. Require strict mapping errors for unknown fields.
- [ ] Index a valid 4,096-byte page containing emoji, combining marks, CJK, Greek, Markdown, URLs, punctuation, long words, and newlines. Compare the complete source and final character. Inspect every native chunk and embedding. Require bounded forward progress without reproducing the model tokenizer in Tack or validation code.
- [ ] Index 4,096 newlines. Require bounded forward progress and native sparse output for every nonempty generated chunk. Never infer reader completion from a chunk count.
- [ ] Replace a same-ID document with `{"retired":true}` at the retirement version. Require no text or semantic fields and no active search match. Accept active empty text without inference. Reject a missing required `page_text`.
- [ ] Reject source pages over 4,096 UTF-8 bytes before OpenSearch. Accept complete queries through the query task's byte bound without loading the tokenizer in Tack.
- [ ] Use the official test-only `ConnectionObserver` to prove every adapter request uses the configured environment endpoint. Do not add application-side node discovery, a custom transport, or an exact route-count requirement.
- [ ] Record bundle size, reported runtime memory, process memory, peak ingest memory, and inference latency in an 8 GiB container. Preserve the 4 GiB circuit-breaker regression. Do not infer concurrent capacity from this test.
- [ ] Run native tests and `make check`. Commit with subject `Add native OpenSearch sparse indexing validation` through the signed procedure.

The query task proves one-time query inference, relevance, and complete continuation. The deployment task proves single-node recovery in both initial environments and requires separate scale-out acceptance before production claims failover.
