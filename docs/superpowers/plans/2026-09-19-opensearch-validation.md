# OpenSearch prototype and validation record

This record accounts for the experiments that selected the OpenSearch design. Each
experiment used a disposable local OpenSearch 3.8.0 environment. The results prove
the stated engine behavior only. Tack integration, FoundationDB recovery, single-node
QA and production operation, later production scale-out, authorization, and production
capacity remain acceptance work.

## Text coverage prototypes

| Prototype | Observation | Decision |
| --- | --- | --- |
| Direct dense embedding | The first pinned model ignored appended text after token 128. Appending a new token after 126 repeated tokens produced no vector change, while replacing the last accepted token changed the vector. | Rejected. A reader page cannot be submitted as one model input. |
| Native word splitting | A configured 384-word segment required 2,306 model tokens. Punctuation-only and whitespace-only inputs also returned an empty-documents error. | Rejected. Word counts do not enforce the model limit. |
| Native fixed-character splitting | OpenSearch processed 647 segments. The 32-character setting stayed below the model limit, but an emoji boundary produced unpaired surrogate escapes in stored segment text. Overlap preserved another complete copy of the emoji. | Rejected. Stored generated text must remain valid Unicode. |
| A 96-byte Tack reader page | Thirteen adversarial inputs reconstructed exactly, produced finite vectors, and used at most 98 model tokens. | Kept only as a diagnostic control. It did not prove the required 4,096-byte page and would make Tack's page size depend on one model. |
| Custom pipeline from native processors | A `gsub`, delimiter chunking, and dense embedding pipeline preserved all 20 text cases. A 4,096-byte newline page created 8,377 vectors. | Rejected after the native `semantic` field provided the needed mapping without an application-defined pipeline. |
| Native `semantic` field with GTE sparse encoding | One 4,096-byte Unicode page retained its complete source and final character. OpenSearch created 27 chunks and 27 sparse embeddings. The largest chunk contained 152 characters, and the largest start step contained 76 characters. | Selected. Tack enforces a byte bound. OpenSearch owns chunking and document embedding. |

The early direct, word, character, and 96-byte tests used the 1.0.2 dense model
bundle recorded in `evidence.tar.gz`. The selected design uses
`amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0.
Results from the dense model do not set the GTE model's limits.

## Ranking and continuation prototypes

| Prototype | Observation | Decision |
| --- | --- | --- |
| Approximate hybrid query with `k: 10000` and collapse | The control returned 150 distinct nodes. Adding 36,000 matching pages for one node reduced the result to one distinct node. The fixed candidate queue filled before collapse could retain other nodes. | Rejected. Increasing a fixed candidate count cannot support an arbitrary page count. |
| Exact vector scoring inside the hybrid collector | The control returned 150 distinct nodes, but the same duplicate-heavy corpus still returned one. | Rejected. Exact scores did not remove the hybrid collector's candidate window. |
| Exact dense scoring inside a Boolean query | Ordinary collapse returned all 150 nodes. All 12 relevance targets ranked within the first eight distinct nodes among 162 nodes. Five runs with 36,000 duplicate pages preserved order. Median latency was 127.7 ms and the maximum was 243.1 ms on two CPUs. | Passed correctness, then rejected for release. Query work scanned every permitted vector and grew directly with indexed pages. |
| Mini sparse model | OpenSearch used `FeatureQuery` operations with no dense script, but six boundary targets ranked 49, 59, 103, 46, 41, and 3. The limit was 25. | Rejected for relevance. |
| Full GTE sparse model through a custom pipeline | All 12 targets ranked within the first 17 distinct nodes; the lexical control found none. A 4,096-byte page produced 243 complete inputs and outputs. Point-in-time traversal returned 37,500 page matches and all 1,501 nodes in 375 batches. | Passed sparse relevance and continuation. The native `semantic` field later replaced the custom pipeline. |
| High-level semantic query | Its built-in analyzer omitted both `signin` targets. It also repeated inference for every request. One hundred queries took 15.4 seconds, with a 148.3 ms median. | Rejected. Continuation cannot repeat model inference. |
| One GTE prediction plus saved `query_tokens` | One prediction returned the accepted targets at ranks 1 and 7 for `db`, 15 and 18 for `signin`, 1 and 2 for `lag`, 1 and 3 for `invoice`, 4 and 13 for `crash`, and 1 and 2 for `remove user`. Three repeats preserved order. | Selected. FoundationDB stores the opaque token-weight map for the search session. |
| Native semantic pages with point-in-time traversal | The final field mapping returned 37,500 page matches and all 1,501 distinct node IDs in 375 batches of 100. One node contributed 36,000 pages. Traversal took 4.786 seconds after one query inference. | Selected. A point in time, `search_after`, and durable visited-node records replace a total-result cap. |

The pagination fixture copied one small sparse value into the 37,500 documents to
isolate traversal from document inference. The relevance and page-coverage tests used
the real GTE model. These two tests answer different questions and must not be
combined into a production throughput claim.

## Boundary and failure experiments

| Experiment | Observation | Plan consequence |
| --- | --- | --- |
| Query processor input substitution | JSON-array substitution produced the same 384 dense values as direct inference and accepted quotes, backslashes, newlines, and emoji. The `_toString()` alternative embedded an unresolved literal and failed relevance. | The dense control used structured substitution. The final sparse design avoids the request processor and calls one concrete prediction request. |
| Authorization filters | Five foreign organization, scope, and retirement controls were absent from the accepted relevance results. An alternate scope returned only its two eligible nodes. | Keep structured engine filters and require a current FoundationDB authorization check before returning a node. |
| Empty, retired, and missing text | Active empty text and retirement records indexed without embeddings. A document missing required text returned HTTP 400. | Preserve explicit active, retired, empty, and malformed cases in native integration tests. |
| Dense semantic storage settings | The old dense semantic mapping rejected `index.knn: false` with either the default or explicit embedding method. | This result applies only to the rejected dense control and does not constrain the selected sparse mapping. |
| Point-in-time mutation isolation | Creates, edits, and deletes after opening the point in time did not change the established traversal. A fresh traversal observed the edits. Deleting the point in time produced HTTP 404. | Bind each durable session to one point in time and return an explicit restart error when it disappears. |
| Response boundary and repeat | Stopping before one candidate preserved that candidate for the next response. An independent run returned all 1,511 nodes in the edited corpus through 19 bounded requests. | Persist the exact last consumed sort value and stop only after an empty raw engine batch. |
| Sparse failure responses | Strict mapping returned HTTP 400. A missing model and deleted point in time returned HTTP 404. | Preserve engine failures as explicit adapter errors instead of reporting an empty successful search. |

## Client, memory, and shard experiments

| Experiment | Observation | Decision |
| --- | --- | --- |
| GTE memory at 4 GiB and 8 GiB | The 4 GiB container opened the ML memory circuit breaker during the duplicate-heavy traversal. The equivalent 8 GiB run completed and used about 3.4 GiB afterward. | Require at least 8 GiB for every guest that runs the model. Measure peak host memory during the complete workload. |
| Official Go client v4.7.3 against OpenSearch 3.8.0 | The client completed index creation, bulk indexing, refresh, point-in-time creation and deletion, search request execution, alias changes, index and document reads, index deletion, metrics, and close. Stable v4 lacks typed ML Commons APIs and its typed search response does not preserve replacement point-in-time IDs and exact sort JSON. | Use typed core APIs. Use narrow `opensearch.Request` and response types through `opensearch.Do` and `opensearch.ParseError` for the missing fields and ML Commons. |
| Native split with the GTE model undeployed | One source shard with eight reserved routing shards split to two green primaries. Five documents, seven generated chunks, every sparse weight, and the saved raw-sparse query matched exactly. The serving alias remained on the source until one atomic switch. | Use native split for a pure primary-shard increase. Existing embeddings are reused. |
| Repeated native split | The same index split from one to two, four, and eight primaries while the model remained undeployed. The typed v4.7.3 `Indices.Split` request succeeded. After model redeployment, the target accepted and embedded a new document. | Reserve the routing path at index creation and keep split inside the existing replacement coordinator. |
| Combined lexical and sparse ranking after split | Result order remained the same, but numeric scores changed because lexical scoring uses shard-local term statistics. | Rerun relevance and continuation after a split. Do not require identical combined scores. |
| Existing infrastructure endpoint feasibility | Both Tack application guests in production reached `3d06:bad:b01::254`; both QA guests reached `3d06:bad:b01:210::5`. Port 9200 was unused on both hypervisors. Configs already deploys systemd services to both. The existing production Traefik 3.0 process was active with zero restarts and about 54 MiB resident memory, but it is one production-only LXC. | Run a separate Traefik service on each hypervisor. Tack receives one endpoint. Each proxy starts with one backend and accepts later backends without changing Tack. Do not route QA through the production proxy LXC. |
| QA host memory projection | Suburban has 31.31 GiB usable memory, no swap, and a 10.22 GiB one-week minimum available. A 20 percent reserve requires 6.26 GiB available. The earlier 3.4 GiB post-workload OpenSearch reading plus a 0.125 GiB proxy would leave about 6.69 GiB, but the guest can consume up to 8 GiB and the reading did not measure the peak. The motherboard reports a 32 GB maximum with every slot populated. | Provision one capped 8 GiB QA guest. Keep it enabled only after the complete workload proves that host available memory never falls below 6.26 GiB. Reject multi-node QA on suburban. |
| QA host CPU projection | The Xeon E3-1230 V2 provides four cores and eight threads. One-week p95 CPU use was 29.33 percent, or 2.35 thread equivalents. One two-vCPU search guest requires 5.43 total threads with 20 percent reserve. | The single-node QA topology passes the CPU projection. |
| QA fast-storage projection | The fast pool has 457.41 GiB total and 215.92 GiB available. One 40 GiB guest leaves 175.92 GiB, or 38.46 percent. | The single-node QA topology passes the fast-pool reserve. Do not place latency-sensitive search data on the slow pool. |

## Investigated approaches that did not reach a prototype

- Raising the 100 MB HTTP request limit, the nested-object limit, or the native
  chunk-count limit would still process one large OpenSearch document. It would not
  provide the reader's resumable, unbounded page contract.
- A custom OpenSearch plugin was investigated as a way to access the exact model
  tokenizer and create multiple documents. The available interfaces did not prove
  exact tokenizer access, and maintaining custom OpenSearch code exceeded the
  accepted scope. No plugin or fork was built.
- The v5 Go client preview included more generated APIs but was still prerelease.
  The validated plan uses stable v4.7.3 and narrow types for missing operations.

## Evidence archives

The raw programs, requests, responses, profiles, rules, and failure outputs remain
in local evidence archives rather than this documentation change. The hashes identify
the exact archives used here. Implementation acceptance must reproduce the required
behavior through Tack's public boundaries.

| Archive | SHA-256 | Contents |
| --- | --- | --- |
| `evidence.tar.gz` | `08e73bb41841eac6048f4d3a3f03987226274a0a82344ec9a664e8c9c22e587f` | Direct, word, character, 96-byte, and early hybrid experiments |
| `resolution.tar.gz` | `8a45d96662e840f0d02548662377fb12355f8f45971543b256223402a216af7c` | Hybrid starvation and exact dense controls |
| `pagination.tar.gz` | `88b0adf3f9839b4b35ceeec8c3b6ebf78e0a4ce5c47a6ac51be933caed4c67e5` | Pagination beyond 1,000 nodes and live-index controls |
| `sparse.tar.gz` | `2c6c996795ef0cd12c55a5c299e3f640c5cc230ef1672c8b069671f0715ba31c` | Mini sparse failure, GTE sparse success, memory, and error cases |
| `native-semantic-client-audit.tar.gz` | `17fcea34be605e9268664c35a6bf563a75129e82d5d3b22fa14e13b9aa2f0182` | Native semantic field, query reuse, pagination, and Go client audit |
| `native-opensearch-split-audit-2026-09-20.tar.gz` | `5fa7d0edae571f067b800b4da3a8da6d493bede488465dca228bcf81e914a810` | One-to-two split, repeated split path, alias switch, and typed client call |
| `search-endpoint-feasibility-2026-09-20.tar.gz` | `66068ae4a7a7aea4a49d56bd04b6509c22c85d1cbcf82918c5a1cb828509dbe0` | Current configs revision, DNS, live host memory, app-to-hypervisor reachability, free listeners, guest resources, and the existing Traefik process |
| `qa-capacity-projection-2026-09-20.tar.gz` | `1daedefe0fb5172be60a83ba2554789fb733a24749293cdf927c94b8c244c935` | One-week memory and CPU bounds, physical hardware limits, active guest allocations, fast-pool capacity, and initial and scale-out calculations |
| `qa-single-node-capacity-2026-09-20.tar.gz` | `0abc8060f8ad4bb8eca9b423e33e816b3776fea6d8166de9d593af26ab2fbde2` | Selected single-node QA topology, live memory gate, CPU and storage projections, and rejected multi-node claims |

Future experiments must add their question, setup, observed result, plan consequence,
raw artifact name, and SHA-256 here before the plan or PR claims the result.
