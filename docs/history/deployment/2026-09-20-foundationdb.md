# Earlier FoundationDB expansion observations

The September 20, 2026 expansion plan reports these QA observations. They explain the plan's sequence and do not establish current configuration.

The app, audit consumer, backup agent, and ops sidecar mounted `/etc/foundationdb` read-only. The startup script rewrote the cluster file to `docker:docker@fdb:4500`, and its modification time matched the container's most recent start. The plan also identified a new-guest startup path that created a separate cluster when the guest lacked the existing cluster file.

The original owner guest advertised a Docker bridge address that other guests could not use. After the data guests joined, FoundationDB recruited transaction logs and commit proxies across processes that could not all connect to each other. Recovery remained at `initializing_transaction_servers` for 15 minutes. After the data-guest processes stopped, recovery completed in 45 seconds.

The [original expansion plan](https://github.com/agoodkind/tack/blob/7e469f9851c55e0ae36a03fe279287c6a430da05/docs/superpowers/plans/2026-09-20-store-three-data-guests.md) used those observations to require exclusion of the Docker bridge process before another data guest joined. The observations do not prove that a later deployment completed that migration.
