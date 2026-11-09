# Search cluster and capacity criteria

Search releases must pass these criteria through public operations with real FoundationDB, OpenSearch, the deployed model, and authentication. Storage expansion must repeat them at larger node sizes. A criterion without observed evidence remains unverified. The [evidence and environment requirements](2026-09-19-search-acceptance.md#evidence-and-environment) apply to these criteria. QA acceptance must precede production. Production tests require explicit operator authorization.

## Cluster and capacity

- The acceptance test must verify the actual initial QA and production topology against reviewed configuration. Acceptance requires one combined-role guest each, the OpenSearch 3.8.0, verified REST and transport TLS, IPv6-only access, least-privilege application credentials, zero replicas, QA single-node discovery (`discovery.type: single-node`), and normal production discovery. Configuration alone does not prove those live facts.

The accepted initial topology requires the specified resources per model guest.

Initial resource requirements:

| Resource | Requirement |
| --- | --- |
| Memory | At least 8 GiB. |
| Logical CPUs | Two. |
| Fast storage | 40 GiB. |
| JVM heap | 3 GiB. |

The capacity test must record these measurements:

- Model bundle size.
- Reported inference memory.
- JVM heap.
- Process memory.
- Filesystem use.
- Peak ingest and host memory.
- Latency.

Earlier runs established different memory outcomes.

Historical memory observations:

| Guest memory | Observation |
| --- | --- |
| 4 GiB | The run tripped the model memory circuit breaker. |
| 8 GiB | The run completed and used about 3.4 GiB afterward. |

Peak workload memory still requires measurement.

- The acceptance test must use one HTTPS endpoint per environment on its hypervisor guest segment. Acceptance requires the official observer to record only that endpoint. The acceptance test must verify its certificate, backend certificate checks, authenticated readiness, caller restrictions, and one initial backend. Before application cutover, during a fixed number of production queries, proxy records must show the healthy backend accepting requests.
- The acceptance test must stop each search guest during source writes. Acceptance requires committed FoundationDB writes, explicit search outage, and pending work. After restart, acceptance requires authenticated readiness and a searchable backlog. Before member-stop tests and after each restart, the acceptance test must verify every eligible predictor and matching cached worker and target identities. Shard health alone is insufficient. The acceptance test must preserve the first public error even if later requests succeed.
- Acceptance requires ML-only inference placement and least-load dispatch. The acceptance test must deploy the model without `node_ids`, require `DEPLOYED` on eligible members after provisioning and restart, and preserve native automatic redeployment. The acceptance test must verify the optional bounded repair gate separately. While repair and public search are enabled, the acceptance test must test a model in `PARTIALLY_DEPLOYED` or `DEPLOY_FAILED` without a running deploy task for 30 seconds. Acceptance requires one process to claim the repair and at most three attempts per stuck episode. Acceptance requires disabled public search to prevent repair requests.
- Before production scale-out, the operator must join members to the existing cluster without another initial bootstrap. The acceptance test must add healthy proxy backends and verify distribution. The acceptance test must set one replica only after at least two data nodes join, wait for green, record replicas with the physical generation, and preserve sparse weights. The acceptance test must split only along reserved routing shards when measured load requires it. With at least three members and one replica, the acceptance test must stop each member independently and require endpoint, primaries, inference, search, and indexing availability.
- The acceptance test must alternate a complete QA session between two actual Tack processes, including replay, revocation, replacement, and cleanup without sticky routing. The acceptance test must verify stable hash buckets and bounded scans. A fixed workload must not serialize on one global counter, lease, queue, or key range. The acceptance test must add Tack processes and FoundationDB capacity independently under fixed request and worker workloads and require improvement without format changes.

Before capacity runs, the capacity test must declare these inputs:

- Corpus size.
- Page distribution.
- Query mix.
- Concurrency.
- Mutation rate.
- Ingestion bursts.
- Rebuild activity.
- Duration.
- Documented product thresholds.

The test must measure these outcomes:

- p50, p95, and p99 latency.
- Errors.
- Completed throughput.
- Freshness.
- Oldest work age.
- CPU.
- Peak memory.
- Disk.

The test must explicitly record a missing product threshold. Offered rates do not establish thresholds.


The capacity test must establish a search-only baseline with `ops qa datagen search-load` using distinct first-page queries at each offered rate.

Offered rates:

| Run | Requests per minute |
| --- | ---: |
| 1 | 1000 |
| 2 | 3000 |
| 3 | 5000 |

The test must count these request outcomes separately from committed writes:

- Offered.
- Sent.
- Completed.
- Dropped.
- Failed.

The test must classify errors and report completed requests per minute and latency percentiles.

- The acceptance test must repeat those rates through real public calls during creates, edits, deletes, permissions, bursts, one rebuild, an isolated outage, and catch-up. The acceptance test must alternate a continuation between two Tack processes during load. The acceptance test must search the same workload's content with unique revision markers and measure freshness from commit to the first returned marker.
- The acceptance test must run bulk import with ordinary edits and sustained queries. The acceptance test must continue after import until the oldest pending work completes. The acceptance test must include lexical and semantic variation, small and large documents, several tenants, and index maintenance. The acceptance test must search and replay while deletion or revoked-permission work remains pending. The acceptance test must fail on any disclosure.
- The acceptance test must compare mixed-workload search latency, errors, throughput, committed writes per minute, freshness, work age, CPU, memory, and disk with the search-only baseline at the same offered rate. The acceptance test must measure both the search guest and hypervisor. The acceptance test must reuse the process-test session and update machinery for reads of 140 unchanged nodes and updates to separate nodes, but must not derive search-capacity claims from `TestSearchActualOSProcessThroughput` or ordinary `ops qa datagen soak`.

The complete QA workload must meet the host memory gate.

Host memory requirement:

| Host | Available memory |
| --- | --- |
| `suburban` | At least 6.26 GiB during the complete QA workload. |

The capacity test must read these counters:

- The host's `/proc/vmstat` `oom_kill` counter.
- Each Tack guest's cgroup `memory.events` `oom_kill` counter.

Any counter increase, sustained full memory pressure, or repeated telemetry-read failure requires the test operator to stop the workload by sending `SIGTERM` to the running search-load or search-mixed command. Acceptance requires a repeat of the entire workload after an interruption.

- The acceptance test must keep QA to one combined-role search guest on `suburban`. The implementation must not create temporary ML-only or data-only guests there. The acceptance test must keep the guest enabled only after the complete QA indexing, query, and rebuild workload satisfies the host memory gate. The acceptance test must measure production capacity separately on `vault`. Initial single-node results do not establish failover or increased OpenSearch capacity.

The capacity test must measure the disk inputs at the specified stages.

Disk measurements:

| Input | Measurement stage |
| --- | --- |
| Non-index bytes | After provisioning. |
| Primary-index bytes | After rebuilding. |

Initial disk capacity must satisfy `(non-index bytes + 3 * primary-index bytes) / 0.85` for serving, replacement, and retiring generations without replicas. The test must recalculate per-node disk before adding replicas. Model download size and one successful query do not establish disk capacity.
