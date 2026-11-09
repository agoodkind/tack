# How search uses source data and indexed pages

FoundationDB stores Tack nodes and the metadata that determines searchable text. OpenSearch stores a derived index. A full replacement reads FoundationDB and creates an empty target index. Search recovery does not require another search engine's data.

## Separate source writes from indexing

When Tack has a configured search endpoint, FoundationDB records the node change and its desired search work in the same transaction. Workers process that work after the transaction commits. An OpenSearch outage leaves work pending while FoundationDB continues to accept source writes.

Workers read revision-bound pages and save progress after accepted writes. A later claim resumes unfinished work. Generation checks reject an older content, access, or retirement request after a newer change. Retirement removes old text but retains a record that prevents a delayed writer from restoring it.


## Apply metadata without product-specific extraction

Stored declarations choose included text, exclusions, representation, and order. A property type supplies a default only when the definition omits a declaration. Explicit exclusions take precedence over defaults. Property names and the FoundationDB secondary-index flag do not select searchable content.

Rendering indexes the node name and valid content even when another property has an invalid value. It preserves the invalid source value and records a separate warning. A malformed declaration prevents rendering. Failed source reads and engine requests leave work pending and return explicit errors.

Metadata changes schedule affected content against the serving index. Access changes update opaque access values without sending text to the model. These operations avoid replacing the physical index when its model and mapping still apply.

## Rank pages and return nodes

OpenSearch creates sparse embeddings, which map model tokens to numeric weights. Its native semantic field preserves original text and generates chunk embeddings. Tack enforces the reader's byte limit and does not implement the model tokenizer.

Tack computes sparse query weights when it opens a session. FoundationDB stores those weights for later pages. Every continuation reuses them without another inference request.

The original `bert-uncased` analyzer comparison omitted both `signin` targets. The regression requires saved model weights to return both targets among the first 25 distinct nodes, and the analyzer to miss at least one. A separate control repeated inference on every request. Its 100 queries took 15.4 seconds with a 148.3 ms median. The current release must repeat the comparison and measure continuation with saved weights.

One node can contribute many matching pages. A fixed candidate window or field collapse can discard other nodes before complete traversal. Tack records visited node IDs separately from the engine sort position. A point in time freezes the engine view, and `search_after` resumes after the last consumed match. Only an empty raw batch completes traversal.

## Check permission at both boundaries

One permission policy compiles opaque values for indexed resources and callers. OpenSearch filters forbidden pages before ranking. Current FoundationDB checks reject deleted nodes and revoked access before Tack returns a result or replays a page.

An access-policy transition writes candidate and active versions together. Exact page verification precedes activation. Existing sessions retain their version until they finish. Cleanup then removes old keys. The transition preserves text and sparse weights without replacing the index.

## Replace physical indexes safely

A full replacement copies current source nodes. A permitted primary-shard increase uses native split and preserves embeddings. During copying, verification, and switching, search writes also update the target. The switch waits for copies and current-page verification before changing the alias. The coordinator does not replay a mutation journal.

New sessions use the replacement after the switch. Existing sessions continue against the old physical index until their deadlines. Bounded cleanup removes session records before deleting the retiring index. Retirement and size thresholds start full replacements because splitting preserves obsolete documents.

## Distinguish redundancy from capacity

A single search guest cannot provide search-node failover. The two-process test measures Tack scaling. The proxy and search members in each initial environment depend on the same hypervisor. The proxy selects a healthy backend. OpenSearch assigns inference to eligible ML nodes and search to primary or replica shards. Dedicated ML nodes add inference resources, data nodes and replicas add ranking resources, and coordinating nodes can serve as proxy backends without changing Tack. Additional OpenSearch members require separate model-placement, proxy, replica, and independent member-stop tests.

Model memory requirements and a successful request do not establish production capacity. Capacity evidence must measure public search and concurrent writes, freshness, pending-work age, memory, disk, maintenance, outage, and catch-up on the target host. Configuration and merged changes do not establish live acceptance.
