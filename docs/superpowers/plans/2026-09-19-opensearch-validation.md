# OpenSearch prototype and validation record

Disposable local OpenSearch 3.8.0 experiments selected this design. Their results
establish only the measured engine behavior, not Tack integration, FoundationDB
recovery, authorization, deployed single-node operation, later scale-out, or
production capacity.

## Text coverage prototypes

The coverage experiments compared these inputs and mappings:

| Prototype | Observation | Decision |
|---|---|---|
| Direct dense embedding | The first pinned model ignored text after token 128. A token after 126 repeated tokens did not change the vector; replacement of the last accepted token changed it. | Reject a whole reader page as one model input. |
| Native word splitting | A 384-word segment required 2,306 tokens. Punctuation-only and whitespace-only inputs returned empty-documents errors. | Reject word counts as enforcement of the token limit. |
| Native fixed-character splitting | OpenSearch processed 647 segments. A 32-character limit fit the model but produced unpaired surrogate escapes at an emoji boundary. Overlap preserved another complete emoji copy. | Reject invalid Unicode in generated stored text. |
| A 96-byte Tack reader page | Thirteen adversarial inputs reconstructed exactly with finite vectors and at most 98 tokens. | Retain as a diagnostic control only. It does not prove 4,096-byte coverage and makes pagination model-dependent. |
| Custom native-processor pipeline | gsub, delimiter chunking, and dense embedding preserved 20 text cases. A 4,096-byte newline page produced 8,377 vectors. | Prefer native semantic mapping over an application-defined pipeline. |
| Native semantic field with GTE sparse encoding | A 4,096-byte Unicode page preserved complete source and final character. OpenSearch generated 27 chunks and 27 embeddings; maximum chunk length was 152 characters and maximum start increment was 76. | Use native semantics. Tack bounds bytes; OpenSearch chunks and embeds. |

Early direct, word, character, and 96-byte experiments used dense model bundle
1.0.2 in evidence.tar.gz. The selected model is
`amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0.
Dense results do not establish GTE limits.

## Ranking and continuation prototypes

The ranking experiments compared these queries:

| Prototype | Observation | Decision |
|---|---|---|
| Approximate hybrid query with k: 10000 and collapse | The control returned 150 distinct nodes. A corpus with 36,000 matching pages for one node returned only that node; the candidate queue filled before collapse. | Reject fixed candidate counts for arbitrary page counts. |
| Exact vector scoring in the hybrid collector | The control returned 150 nodes, but duplicate-heavy input still returned one. | Reject exact scoring inside the fixed collector window. |
| Exact dense scoring in a Boolean query | Collapse returned all 150 nodes. All 12 relevance targets ranked within the first eight of 162 nodes. Five 36,000-duplicate runs preserved order. Median latency was 127.7 ms and maximum 243.1 ms on two CPUs. | Correctness passes, but reject release because work scans every permitted vector and scales directly with pages. |
| Mini sparse model | FeatureQuery did not use dense scripts, but six targets ranked 49, 59, 103, 46, 41, and 3 against limit 25. | Reject this model for relevance. |
| Full GTE sparse model through custom pipeline | All 12 targets ranked within the first 17 nodes; lexical control did not find them. One 4,096-byte page produced 243 complete inputs and outputs. Point-in-time traversal returned 37,500 page matches and all 1,501 nodes in 375 batches. | Sparse relevance and traversal pass; replace the custom pipeline with native semantic mapping. |
| High-level semantic query | The analyzer omitted both signin targets and repeated inference per request. One hundred queries took 15.4 seconds, median 148.3 ms. | Reject repeated inference during continuation. |
| One GTE prediction with saved query_tokens | Target ranks were db 1/7, signin 15/18, lag 1/2, invoice 1/3, crash 4/13, and remove user 1/2. Three repetitions preserved order. | Save opaque prediction weights in the FoundationDB session. |
| Native semantic pages with point-in-time traversal | The final mapping returned 37,500 matches and 1,501 distinct nodes in 375 batches of 100, including 36,000 pages for one node. Traversal took 4.786 seconds after one inference. | Use point-in-time search_after with durable visited-node records rather than a total-result cap. |

The 37,500-document pagination fixture copied one small sparse value to isolate
traversal from inference. Relevance and page coverage used the real GTE model.
Do not combine those measurements into a production throughput claim.

## Boundary and failure experiments

The boundary experiments establish these observed behaviors:

| Experiment | Observation | Implementation requirement |
|---|---|---|
| Query processor substitution | JSON arrays matched direct inference's 384 dense values and accepted quotes, backslashes, newlines, and emoji. _toString() retained an unresolved literal and failed relevance. | Use structured substitution for the dense control; final sparse search uses one concrete prediction request without this processor. |
| Authorization filters | Accepted results omitted five foreign-org, scope, and retirement controls. Another scope returned only its two eligible nodes. | Use structured filters plus current FoundationDB authorization. |
| Empty, retired, and absent text | Active empty text and retirement indexed without embeddings. Missing required text returned HTTP 400. | Test each case and preserve explicit malformed-input failure. |
| Dense semantic settings | The dense mapping rejected index.knn: false with default and explicit methods. | Do not apply dense-control restrictions to sparse mapping. |
| Point-in-time isolation | Later creates, edits, and deletes did not alter the established traversal; a fresh traversal observed them. A deleted point in time returned HTTP 404. | Bind sessions to one snapshot and return an explicit restart error after its deletion. |
| Response boundary and repetition | A stop before one candidate preserved it for the next response. An independent edited corpus returned 1,511 nodes through 19 bounded requests. | Persist exact consumed sort values and finish only after an empty raw batch. |
| Sparse failures | Strict mapping returned HTTP 400; missing model and deleted point in time returned HTTP 404. | Return adapter errors rather than successful empty results. |

## Client, memory, shard, and host experiments

The resource and compatibility experiments produced these results:

| Experiment | Observation | Decision |
|---|---|---|
| GTE memory at 4 GiB and 8 GiB | The 4 GiB container triggered the ML circuit breaker during duplicate-heavy traversal. The equivalent 8 GiB run completed and used about 3.4 GiB afterward. | Require 8 GiB per model guest and measure host peak during the full workload. |
| Official client v4.7.3 with OpenSearch 3.8.0 | Index creation, bulk, refresh, point-in-time creation/deletion, search, aliases, document/index reads, deletion, metrics, and close worked. Stable v4 lacks typed ML Commons and does not preserve replacement PIT IDs or exact sort JSON. | Use typed core APIs and narrow request/response types with opensearch.Do and opensearch.ParseError for missing data. |
| Native split with model undeployed | One source primary with eight routing shards split into two green primaries. Five documents, seven chunks, all sparse weights, and saved raw-sparse queries matched. The alias stayed on source until one atomic switch. | Use native split for primary-count-only expansion and reuse embeddings. |
| Repeated native split | The index split from one to two, four, and eight primaries without the model. Typed v4.7.3 Indices.Split succeeded. A new document embedded after redeployment. | Reserve the complete routing path at creation and use the existing replacement coordinator. |
| Combined ranking after split | Order stayed identical but numeric scores changed with shard-local lexical statistics. | Rerun relevance and continuation without requiring identical combined scores. |
| Endpoint feasibility | Both production app guests connected to 3d06:bad:b01::254 and both QA guests to 3d06:bad:b01:210::5. Neither hypervisor used port 9200. Configs supports systemd on both. Production Traefik 3.0 used about 54 MiB, had zero restarts, and ran in a production-only LXC. | Run a separate proxy service per hypervisor with one stable endpoint and initially one backend. Do not depend on the production proxy for QA. |
| QA memory projection | Suburban provides 31.31 GiB usable RAM without swap; its one-week minimum available was 10.22 GiB. A 20 percent reserve requires 6.26 GiB. Earlier post-workload OpenSearch at 3.4 GiB plus a 0.125 GiB proxy projects 6.69 GiB available, but the guest can consume 8 GiB and that reading omitted peak use. The motherboard reports 32 GB maximum with every slot populated. | Provision one capped 8 GiB QA guest. Retain it only after full workload keeps at least 6.26 GiB available. Reject multi-node QA on suburban. |
| QA CPU projection | Xeon E3-1230 V2 provides four cores/eight threads. One-week p95 was 29.33 percent, or 2.35 threads. One two-vCPU guest requires 5.43 total threads with 20 percent reserve. | The single-node CPU projection passes. |
| QA storage projection | Fast storage totals 457.41 GiB with 215.92 GiB available. A 40 GiB guest leaves 175.92 GiB, or 38.46 percent. | The reserve passes for one guest. Keep latency-sensitive data off slow storage. |

## Approaches without a prototype

Raising the 100 MB HTTP limit, nested-object limit, or native chunk-count limit
still processes one large document rather than providing resumable reader pages.
The investigation considered a custom plugin for tokenizer access and multiple
documents, but available interfaces did not establish exact access and custom
OpenSearch maintenance exceeded scope. The investigation did not build a plugin
or fork. The v5 client offered more generated APIs but remained prerelease;
validation selected stable v4.7.3 with narrow types for missing operations.

## Preserve experiment evidence

The local archives contain raw programs, requests, responses, profiles, rules,
and failures. Their hashes identify the exact evidence; public Tack operations
must reproduce acceptance rather than relying on those local programs.

| Archive | SHA-256 | Contents |
|---|---|---|
| evidence.tar.gz | 08e73bb41841eac6048f4d3a3f03987226274a0a82344ec9a664e8c9c22e587f | Direct, word, character, 96-byte, and early hybrid experiments. |
| resolution.tar.gz | 8a45d96662e840f0d02548662377fb12355f8f45971543b256223402a216af7c | Hybrid starvation and exact dense controls. |
| pagination.tar.gz | 88b0adf3f9839b4b35ceeec8c3b6ebf78e0a4ce5c47a6ac51be933caed4c67e5 | Pagination beyond 1,000 nodes and live-index controls. |
| sparse.tar.gz | 2c6c996795ef0cd12c55a5c299e3f640c5cc230ef1672c8b069671f0715ba31c | Mini sparse failure, GTE success, memory, and failures. |
| native-semantic-client-audit.tar.gz | 17fcea34be605e9268664c35a6bf563a75129e82d5d3b22fa14e13b9aa2f0182 | Native fields, query reuse, paging, and client review. |
| native-opensearch-split-audit-2026-09-20.tar.gz | 5fa7d0edae571f067b800b4da3a8da6d493bede488465dca228bcf81e914a810 | One-to-two and repeated splits, aliases, and typed calls. |
| search-endpoint-feasibility-2026-09-20.tar.gz | 66068ae4a7a7aea4a49d56bd04b6509c22c85d1cbcf82918c5a1cb828509dbe0 | Configs revision, DNS, host memory, connectivity, ports, guest resources, and Traefik. |
| qa-capacity-projection-2026-09-20.tar.gz | 1daedefe0fb5172be60a83ba2554789fb733a24749293cdf927c94b8c244c935 | One-week memory/CPU, hardware limits, guest allocations, storage, and scale projections. |
| qa-single-node-capacity-2026-09-20.tar.gz | 0abc8060f8ad4bb8eca9b423e33e816b3776fea6d8166de9d593af26ab2fbde2 | Single-node topology, memory requirements, CPU/storage projections, and multi-node rejection. |

Before claiming a new experiment, record its question, setup, observations,
implementation consequence, artifact, and SHA-256 in this record.
