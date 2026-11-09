# Verify ledger node changes

1. Before changing a ledger node's identity or startup configuration, record its
   volume and row count.
2. Verify that the restarted node uses the same volume and returns the same
   count before admitting another node.
3. Before retiring a node, record cluster status from outside the cluster.
   Require the three data nodes, three copies, and full replication.
