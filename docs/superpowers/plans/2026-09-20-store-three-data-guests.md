# Spread the product store across three data guests

The product store runs one FoundationDB process on the owner guest and keeps
one copy of every key. Losing that guest loses product data. Correct these
QA-verified defects before expansion:

1. The app, audit consumer, backup agent, and ops sidecar mount
   `/etc/foundationdb` read-only and cannot save coordinator changes.
2. Startup rewrites the cluster file to one entry. On tack-qa,
   `/etc/foundationdb/fdb.cluster` contains `docker:docker@fdb:4500` and its
   modification time matches the last container start.
3. Startup on a guest without a cluster file creates a new coordinator and
   cluster instead of joining the existing cluster.

The QA coordinator uses container address `[3d06:bad:b01:210:7ac::b]:4500`.
Tack-data1 cannot ping or connect to it, although tack-qa can. Published guest
ports work across guests: tack-data1 listens on `[::]:5433`, and tack-qa connects
to `[3d06:bad:b01:210::220]:5433`.

FoundationDB requires a running process at a new address before `coordinators`
can select it. Restarting a lone coordinator at a different address cannot
change the recorded address. Start a process at a cross-guest address first.

## Correct mounts, cluster files, and identity

These code changes do not themselves change the running cluster:

- Mount `/etc/foundationdb` writable in `docker-compose.yml` for the app,
  audit consumer, backup agent, and ops sidecar.
- In `fdb-overlay/fdb.bash`, preserve a present, non-empty cluster file.
  Make server and clients share `/etc/foundationdb/fdb.cluster` rather than
  storing a separate server copy at `/var/fdb/fdb.cluster`.
- Before starting processes, copy the live cluster file to guests that lack it.
  Select the first participating guest with a store process: the owner while
  `tack_store_legacy_node_present` is true, then the first data guest.
  Stop a deploy without a source unless
  `--extra-var tack_store_bootstrap=true` explicitly permits empty bootstrap.

Read `FDB_PUBLIC_ADDRESS`, `FDB_ZONE_ID`, and `FDB_MACHINE_ID` in the overlay.
Render the pinned address from `service_mapping.yml` and use the inventory
identity for guest fault domains. Container recreation must preserve cluster
identity. Render empty values on guests without a store process to preserve
local development behavior.

Set `transaction_timeout` in `foundationdb.Open` from
`FDB_TRANSACTION_TIMEOUT`, default five seconds. API version 740 preserves that
option across retries and bounds the complete retry loop. Previously, two
caller-deadline sites set timeouts while other transactions retried indefinitely.

Run audited `ops store status`, `set-redundancy`, `set-coordinators`, and
`exclude` through one-shot `fdbcli` containers using the local cluster file,
including on guests without a store container. `ops provision` uses that method
and reads `TACK_OPS_FDB_REDUNDANCY_MODE` rather than hardcoding `single`.
A deploy with `tack_store_node_present` false everywhere must not start store
processes or change the cluster.

## Rehearse before migration

Step 3 changes the coordinator irreversibly. A wrong address makes every
client unable to connect until an operator corrects four cluster files.
Before changing QA's store, create a throwaway cluster on the QA owner with
two processes, a scratch volume, and the same script and environment. Change
coordinators and verify client-file updates. Rehearse step 4 exclusion and
step 5 joining there too; both failed during the original QA migration.

Every cluster process must connect to every other process. The owner bridge
process announces an address that only its guest can access. If it remains
eligible when data processes join, the master recruits transaction servers
that cannot communicate and recovery stops at `initializing_transaction_servers`.
On QA it remained there for fifteen minutes, then recovered in 45 seconds after
the data processes stopped. Exclude the bridge process before joining data guests.

## Migrate in order

Read each step's result before starting the next.

### 1. Deploy the corrections

On tack-qa, copy the server's address-based cluster file over the shared file
that currently uses Docker service name `fdb`:

```
docker exec tack-fdb-1 cat /var/fdb/fdb.cluster > /etc/foundationdb/fdb.cluster
```

Deploy both repositories at merged refs. Recreate clients with writable mounts
and the store process with the patched script, preserving the copied file.
Require `docker compose run --rm tack-ops ops store status` to report one
process, mode `single`, and one coordinator. Only mounts and startup handling
must change.

### 2. Start a transitional process on the owner

Use host networking and the pinned guest address for this migration-only process:

```
docker run -d --name tack-fdb-join --network host \
  -v /etc/foundationdb:/etc/foundationdb \
  -v /root/tack/fdb-overlay/fdb.bash:/var/fdb/scripts/fdb.bash:ro \
  -v tack-fdb-join-data:/var/fdb \
  -e FDB_NETWORKING_MODE=container -e FDB_PORT=4500 \
  -e FDB_PROCESS_CLASS=unset \
  -e FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster \
  -e FDB_PUBLIC_ADDRESS=3d06:bad:b01:210::217 \
  -e FDB_ZONE_ID=tack-qa -e FDB_MACHINE_ID=tack-qa \
  foundationdb/foundationdb:7.4.6
```

It joins using step 1's cluster file. Require `ops store status` to report two
processes, two zones, mode `single`, and one coordinator.

### 3. Assign the coordinator to the pinned owner address

This step cannot undo the address change automatically:

```
docker compose run --rm tack-ops ops store set-coordinators \
  --addresses 3d06:bad:b01:210::217 --execute
```

Require `ops store status` to list only `[3d06:bad:b01:210::217]:4500` as
coordinator and tack-qa's `/etc/foundationdb/fdb.cluster` to contain that address.
Verify the client file changed without a restart.

### 4. Exclude the bridge process

```
docker compose run --rm tack-ops ops store exclude \
  --addresses 3d06:bad:b01:210:7ac::b --execute
```

The command waits for every key copy to exist on the transitional process.
Require status to show one serving process and excluded
`[3d06:bad:b01:210:7ac::b]:4500`.

Until step 7, the transitional process is the sole coordinator. Until step 6
finishes replication, it is also the sole data process. Its
`tack-fdb-join-data` volume uses the owner's disk. During QA deploys it twice
exited with `A disk IO operation failed to complete in a timely manner`.
After such an exit, run `docker start tack-fdb-join` on the owner to restore
availability. The volume preserves data across exits.

### 5. Start data guest processes

Set this value in each of the three `tack_dataN_suburban_servers.yml` files,
not in the owner-readable `tack_qa_all.yml`:

```yaml
tack_store_node_present: true
```

Set this value in `tack_qa_all.yml`:

```yaml
tack_store_seed_cluster_file: true
```

Deploy the whole environment including the owner. While
`tack_store_legacy_node_present` is true, the owner supplies the live cluster
file. A data-only deploy cannot find that source and must stop. Each data guest
uses step 3's coordinator, starts one host-networked process, and starts one
backup agent. Require four processes, four zones, mode `single`, one coordinator,
and three backup agents in `ops store status`.

### 6. Set double redundancy

```
docker compose run --rm tack-ops ops store set-redundancy --mode double --execute
```

Require mode `double`, `Replication health - Healthy`, nonzero fault tolerance,
and `Moving data` equal to zero before continuing.

### 7. Assign coordinators to the data guests

```
docker compose run --rm tack-ops ops store set-coordinators \
  --addresses 3d06:bad:b01:210::220,3d06:bad:b01:210::221,3d06:bad:b01:210::222 \
  --execute
```

Require three coordinators in status and identical coordinator lists in
`/etc/foundationdb/fdb.cluster` on all four guests without restarting containers.
The transitional process stops coordinating.

### 8. Exclude the transitional process

```
docker compose run --rm tack-ops ops store exclude \
  --addresses 3d06:bad:b01:210::217 --execute
```

The command waits until the address stops storing any key copy. Require three
processes, three zones, double redundancy, three coordinators, and three backup
agents. Require both owner addresses in the exclusion list.

### 9. Retire owner store containers

```
docker rm -f tack-fdb-join
docker volume rm tack-fdb-join-data
```

Set in `tack_qa_all.yml`:

```yaml
tack_store_legacy_node_present: false
tack_store_redundancy_mode: double
```

Deploy. The override assigns owner `fdb` and `fdb-backup-agent` to the retired
profile and deployment removes their containers. Future deploys, including
empty QA rebuilds, copy the cluster file from the first data guest.
Require `docker ps` on tack-qa to omit `tack-fdb-1` and
`tack-fdb-backup-agent-1`. Require store status to match step 8.

## Verify acceptance criterion 9

Use running-system readings rather than deployment intent:

- Require three processes, zones, and machines in store status, with each
  process using its data guest's pinned address.
- Require `Redundancy mode - double` and three data guest addresses under
  `Coordination servers`.
- Require `tack-fdb-backup-agent-1` on each data guest and a restorable session
  in store status.
- Require `rw=true` for `/etc/foundationdb` on the app, audit consumer, backup
  agent, and ops sidecar through `docker inspect`.
- Compare `cat /etc/foundationdb/fdb.cluster` on all four guests with status.
- Record container start times, reorder the same three addresses with
  `ops store set-coordinators`, then reread all cluster files and start times.
  Require changed files and unchanged start times.
- Stop each data guest in turn. For two minutes, poll `/healthz` and perform
  an MCP product write and read once per second. Require every non-200 window
  to last at most 10 seconds and a pre-stop acknowledged write to read back.
- Read `ops backup staleness-check` before the failure and fifteen minutes
  afterward. Require a newer FoundationDB restorable point.

Repeat for all three guests. Coordinator and data placement differ, and one
successful guest-failure test does not establish tolerance for the other two.
