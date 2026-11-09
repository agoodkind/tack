# MCP output and search operations

Generated MCP collection tools accept `limit` and `cursor`. Collection limits define the default and valid range.

Collection limits:

| Subject | Limit |
| --- | --- |
| Default | 25 rows. |
| Valid range | 1 through 100 rows. |

Responses print `Next cursor` when another page exists. List cursors resume after a node ID. Ranked search uses durable session cursors.

## Collection and write output

The FoundationDB paged reader returns a page of views and a continuation cursor. Its opaque cursor encodes the last node UUID with URL-safe base64. An invalid cursor fails validation. The reader supports type and property-value scans in node-ID order. It requires a positive limit, applies property and creation-time filters, skips missing views while advancing, and rejects relation scans through this method. Bounded range reads do not impose a total collection cap.

Create, update, and reference-set tools return short confirmations. Confirmations identify nodes and changed fields without repeating full values. Confirmation acceptance and rendering use separate limits.

Confirmation limits:

| Subject | Limit |
| --- | --- |
| Acceptance | 1,024 bytes. |
| Change lines | Up to four. |
| Displayed name | At most 120 bytes of name text, shortened at a Unicode boundary. |

A `More` line reports omitted changes and identifies the generated get tool for the full node.

Workspace descriptions summarize direct children by metadata type.

- The description reads one page of up to 100 direct children per type.
- A trailing plus indicates additional children.

Collection output prints a node name once when that name supplies its displayed identity.

Successful MCP text uses a 32 KiB cap plus its truncation notice. Paged rendering reserves room for continuation. A truncated single-node response omits part of the node. `TestGetIssueCapsHugeDescription` checks truncation across public tools and the storage boundary.

Truncation test sizes:

| Subject | Size |
| --- | --- |
| Multiline issue description from `tack_create_issue` | Just under 60 KiB. |
| FoundationDB value limit | 100,000 bytes. |
| Maximum `tack_get_issue` response | 32 KiB plus the notice's byte length. |

The test creates the issue within FoundationDB's value limit. It passes the returned ID to `tack_get_issue` and requires the response to end with the truncation notice.

## Operator commands

Every command uses shared operator identity, execution gating, audit recording, and result output. This reference does not authorize live operations.

### Provisioning

`ops search provision` validates metadata and topology, provisions the model, and reconciles an empty index and alias. Retrying identical input must be idempotent. The command does not rebuild nodes or enable public search.

### Verification

`ops search verify` checks these targets:

- The command checks the endpoint.
- The command checks the server.
- The command checks the model and local predictor placement.
- The command checks the mapping.
- The command checks the topology.
- The command checks the alias.
- The command checks health.

The command reports these counts:

- The command reports exclusion counts.
- The command reports property-warning counts.
- The command reports stuck-work counts.
- The command reports expired-session counts.

Verification and indexing distinguish these conditions:

- Readiness accepts valid declarations or supported defaults. It refuses malformed declarations and definitions that lack both.
- Property warnings do not fail verification or exclude otherwise valid nodes.
- Authorization exclusions report unresolved access.
- Operational failures retain pending work.
- Stuck-work reporting fails verification after its attempt limit. The report includes kind, ID, attempts, and last error.

`ops search verify` cannot prove these results:

- Semantic relevance.
- Complete traversal.
- Outage recovery.
- Capacity.
- Deployed revision.
- Complete acceptance.

### Reindexing

`ops search reindex` distinguishes these outcomes:

- Preview reports a current or planned replacement.
- Execution begins or resumes full rebuilding or native split with the requested primary count.
- The result identifies the replacement and its current state.
- `--restored` requires full rebuilding and invalidates existing cursors.

### Initial projection backfill

The expiring `ops backfill once-search-projections` command applies a reviewed [initial projection manifest](../../runbooks/search/implement-opensearch-metadata.md).

### Public behavior acceptance

The guarded `ops qa datagen search` command verifies public behavior with opaque metadata. Its modes have separate requirements:

- Preparation exports stored IDs.
- Cohort verification checks manifest cases.
- Committed verification also waits for permanent session audit rows and abandoned-header deletion.

### Search load

The guarded `ops qa datagen search-load` command measures public first-page search at an offered rate. It distinguishes these request outcomes:

- The command counts sent requests.
- The command counts completed requests.
- The command counts dropped requests.
- The command counts failed requests.

### Mixed load

The guarded `ops qa datagen search-mixed` command measures search alongside mutations and maintenance. Full acceptance still requires every declared workload scenario.
