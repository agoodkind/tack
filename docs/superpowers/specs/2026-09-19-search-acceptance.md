# Search on OpenSearch: acceptance

Scope: the design in [Search on OpenSearch](2026-09-19-search-design.md).
Done means every criterion below passes on QA and is then observed on
production. Measurements come from the MCP client, the cluster API, and the
FoundationDB store, never from the index alone.

## 1. Scale

- A project with 150 issues returns 25 rows and a cursor; following cursors
  yields all 150 exactly once.
- One page costs one engine request and one FoundationDB transaction,
  counted in the app's trace spans.
- FAIL if any page reads views one at a time.

## 2. Isolation

- A `node_type` argument that is not a type of the caller's org is refused
  before any request is built, asserted on the request body the client sent.
- Two orgs each index a node with the same title; a search from either org
  never returns the other org's node.
- FAIL if the org filter is absent from any engine request.

## 3. Meaning

- After a reindex on QA, `db` finds `Database failover`, `biscuits` finds
  `Cookie banner`, and five more fixed query and title pairs match, with no
  synonym list in the org.
- FAIL if any pair needs a hand-written word pair to match.

## 4. Operations

- Stop one search guest; search answers within the request timeout and the
  cluster health is yellow, not red.
- Delete the `nodes` index; `ops batch search-reindex --execute` recreates
  it and the meaning pairs match again.
- FAIL if recovery needs any step outside the reindex command and the
  deploy.
