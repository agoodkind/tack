# OpenSearch acceptance plan

OpenSearch still requires production recovery, capacity, relevance, and
scaling acceptance. [Search architecture](../specs/2026-09-19-search-design.md)
and [acceptance criteria](../specs/2026-09-19-search-acceptance.md) define the
required behavior.

## Remaining work

- [Metadata cleanup](2026-09-19-opensearch-metadata.md) requires removal of the
  expiring backfill. TACK-580 supersedes TACK-542's mandatory-declaration
  requirement.
- [Indexing validation](2026-09-20-opensearch-index-pipeline.md) requires
  large-node coverage and measured worker throughput. Tickets: TACK-531,
  TACK-533, and TACK-534.
- [Query validation](2026-09-20-opensearch-query-pipeline.md) requires relevance
  acceptance and measured throughput after adding Tack processes. Tickets:
  TACK-535 and TACK-536.
- [Live validation and correction](2026-09-19-opensearch-final-validation.md)
  requires the complete real-dependency suite and remaining throughput checks.
  Tickets: TACK-518, TACK-519, and TACK-520.
- [Release acceptance](2026-09-19-opensearch-release.md) requires the remaining
  production recovery and capacity evidence. Ticket: TACK-544.

TACK-524 and TACK-525 remain separate storage work that implements the reader
interface. Their implementation reruns the search suite at 128 KiB, 1 MiB,
8 MiB, over 100 MB, and larger than worker memory.

## Validation constraints

Acceptance checks use real dependencies and public boundaries. Mocks, stubs,
recorded responses, private helpers, and product seeds do not establish
acceptance. Real-dependency checks use the [shared fixture
setup](2026-09-19-opensearch-fixtures.md).

Tack owns application behavior, tests, and application configuration. Configs
owns guests, TLS, credentials, inventory, the hypervisor proxy, deployed
environment values, and OpenTofu resources. Deployment and volume deletion
require separate authorization.
