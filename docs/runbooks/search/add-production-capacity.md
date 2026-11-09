# Add production search capacity

An infrastructure apply, application deployment, public activation, source-data repair, and retained-volume deletion have different effects and require their own authorized scope. Preserve authoritative data and audit history throughout capacity expansion. Validate QA before production changes under production authorization. Preserve the applicable authorization and deployment locks. Use the [cluster and capacity criteria](../../reference/search/cluster-capacity-criteria.md#cluster-and-capacity) for topology, replica readiness, workload, and failover requirements. Measure production capacity on `vault` with its own workload and baseline. QA measurements do not establish production capacity.

1. Require measurements that justify expansion and a reviewed configuration change. Join new members through the existing cluster without another `cluster.initial_cluster_manager_nodes` bootstrap.
2. Add healthy members to the stable endpoint and verify each backend accepts requests. Deploy the model without fixed node IDs and verify every eligible predictor. Set one replica only after enough data nodes exist, wait for green, and record the physical generation's replica count.
3. Use native split only for a permitted primary-count increase within reserved routing shards. Require unchanged sparse fields and rerun relevance and continuation.
4. With at least three members and one replica, stop each member independently while search and indexing run. Require endpoint, primary-shard, and local inference availability before claiming failover or added capacity.

