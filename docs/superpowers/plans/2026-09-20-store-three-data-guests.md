# Spread the product store across the three data guests

The product store runs one FoundationDB process on the owner guest, keeping one
copy of every key, and losing that guest loses the product data. Three defects
block any expansion, and all three are live on QA today:

1. Every client mounts `/etc/foundationdb` read-only, so the cluster cannot
   write a coordinator change into a client's cluster file. Measured on
   tack-qa: the app, the audit consumer, the backup agent, and the ops sidecar
   all mount it `ro`.
2. The startup script rewrites the client cluster file to one entry on every
   start. Measured on tack-qa: `/etc/foundationdb/fdb.cluster` reads
   `docker:docker@fdb:4500` and its modification time follows the container's
   last start.
3. A process starting on a guest with no cluster file becomes its own
   coordinator, so a new guest creates a second cluster instead of joining the
   existing one.

Two further facts shape the order below, both measured on QA on 2026-09-20.

The running coordinator is a container address, `[3d06:bad:b01:210:7ac::b]:4500`,
and that address is unreachable from another guest: a ping and a TCP connect
from tack-data1 both fail, while the same address answers from tack-qa itself.
A data guest's process therefore cannot join the cluster at the coordinator the
cluster uses today. Published ports do cross guests on this network: tack-data1
listens on `[::]:5433` and tack-qa connects to `[3d06:bad:b01:210::220]:5433`.

FoundationDB's supported way to change a coordinator's address is to have a
process already running at the new address and then name it with `coordinators`.
There is no supported way to move a lone coordinator by restarting it at a
different address, because the cluster records the address it was told. The
migration therefore starts by adding a process at a reachable address.

## What the code change does

Each defect has one fix, and none of them changes a running cluster by itself.

| Defect | Fix |
| --- | --- |
| Read-only client mounts | `docker-compose.yml` mounts `/etc/foundationdb` writable for the app, the audit consumer, the backup agent, and the ops sidecar. |
| The script rewrites the cluster file | `fdb-overlay/fdb.bash` returns early when the file is present and non-empty, and the server and the clients share `/etc/foundationdb/fdb.cluster` instead of the server keeping a second file at `/var/fdb/fdb.cluster`. |
| A new guest creates its own cluster | The deploy seeds `/etc/foundationdb/fdb.cluster` from `tack_store_bootstrap_coordinators` before any process starts, and only where the file is absent. The deploy starts a store process only where that list names the live coordinators. |

Three more changes come with them.

The overlay reads `FDB_PUBLIC_ADDRESS`, `FDB_ZONE_ID`, and `FDB_MACHINE_ID`.
The deploy renders the guest's pinned address from `service_mapping.yml` and
the guest's inventory name, so the cluster counts guests as fault domains and a
container recreate changes nothing the cluster stored. A guest that runs no
store process renders them empty and keeps the local development behavior.

`foundationdb.Open` sets the `transaction_timeout` database option from
`FDB_TRANSACTION_TIMEOUT`, default five seconds. At API version 740 a retry
does not reset the option, so the one value bounds the whole retry loop. Before
this, two call sites set a timeout from a caller deadline and every other
transaction retried without end.

Four audited `ops store` commands run the migration through one-shot `fdbcli`
containers against this guest's cluster file: `status`, `set-redundancy`,
`set-coordinators`, and `exclude`. They run from a guest with no store
container, which is what the owner becomes. `ops provision` now uses the same
path and reads its redundancy from `TACK_OPS_FDB_REDUNDANCY_MODE` instead of
the hard-coded `single`.

On QA and production the deploy is a no-op until an operator changes the
values in the next section: `tack_store_node_present` is false everywhere and
`tack_store_bootstrap_coordinators` is empty.

## Migration order

Every step is run by an operator and its result is read before the next one
starts.

### 1. Deploy the fixes

Before the deploy, on tack-qa, replace the shared cluster file with the
address the server is using, so the file names an address rather than the
Docker service name:

```
docker exec tack-fdb-1 cat /var/fdb/fdb.cluster > /etc/foundationdb/fdb.cluster
```

Then deploy both repositories at the merged refs. The app, the audit consumer,
the backup agent, and the ops sidecar are recreated with a writable mount; the
store process is recreated with the patched script and keeps the file above.

Read: `docker compose run --rm tack-ops ops store status` reports one process,
mode `single`, one coordinator. Nothing has changed but the mounts and the
script.

### 2. Add a process at a reachable address on the owner guest

The transitional process runs on the owner guest's own network namespace, so it
announces the guest's pinned address. It is one command rather than a compose
service, because it exists only for the length of this migration:

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

It reads the cluster file written in step 1, so it joins the running cluster.

Read: `ops store status` reports two processes and two zones, still mode
`single` and one coordinator.

### 3. Move the coordinator onto the owner guest's pinned address

```
docker compose run --rm tack-ops ops store set-coordinators \
  --addresses 3d06:bad:b01:210::217 --execute
```

Read: `ops store status` names `[3d06:bad:b01:210::217]:4500` as the
coordinator, and `/etc/foundationdb/fdb.cluster` on tack-qa now contains that
address. The second reading is the first proof that a coordinator change
reaches a client file without a restart.

### 4. Start the store process on each data guest

Set in the three `tack_dataN_suburban_servers.yml` files:

```yaml
tack_store_node_present: true
```

Set in `tack_qa_all.yml`:

```yaml
tack_store_seed_cluster_file: true
tack_store_bootstrap_coordinators:
  - "3d06:bad:b01:210::217"
```

The per-guest file is where `tack_store_node_present` belongs, because the
owner guest reads the environment group too. The seed flag and the list change
together: the deploy writes the seeded file when the flag is true, and a file
assembled from an empty list would name no coordinator at all.

Deploy. Each data guest seeds its cluster file with the coordinator from step
3, starts one store process on host networking, and starts one backup agent.

Read: `ops store status` reports five processes and four zones, because tack-qa
runs two of them. Mode is still `single`, with one coordinator and three backup
agents.

### 5. Raise the redundancy to double

```
docker compose run --rm tack-ops ops store set-redundancy --mode double --execute
```

Read: `ops store status` reports mode `double` and, once replication finishes,
`Replication health - Healthy` with a non-zero fault tolerance. Watch `Moving
data` fall to zero before the next step.

### 6. Move the coordinators onto the three data guests

```
docker compose run --rm tack-ops ops store set-coordinators \
  --addresses 3d06:bad:b01:210::220,3d06:bad:b01:210::221,3d06:bad:b01:210::222 \
  --execute
```

Read: three coordinators in `ops store status`, and the same three addresses in
`/etc/foundationdb/fdb.cluster` on all four guests, with no container
restarted.

### 7. Exclude both processes on the owner guest

```
docker compose run --rm tack-ops ops store exclude \
  --addresses 3d06:bad:b01:210::217,3d06:bad:b01:210:7ac::b --execute
```

The command returns only once every copy of a key has moved off both
addresses.

Read: `ops store status` reports three processes, three zones, mode `double`,
three coordinators, three backup agents.

### 8. Retire the owner guest's store containers

```
docker rm -f tack-fdb-join && docker volume rm tack-fdb-join-data
```

Set in `tack_qa_all.yml`:

```yaml
tack_store_legacy_node_present: false
tack_store_seed_cluster_file: true
tack_store_bootstrap_coordinators:
  - "3d06:bad:b01:210::220"
  - "3d06:bad:b01:210::221"
  - "3d06:bad:b01:210::222"
tack_store_redundancy_mode: double
```

Deploy. The override parks `fdb` and `fdb-backup-agent` in the retired profile
on tack-qa and the deploy removes their containers. The bootstrap list and the
redundancy mode now describe the end state, which is what a QA rebuild from
empty needs.

Read: `docker ps` on tack-qa shows no `tack-fdb-1` and no
`tack-fdb-backup-agent-1`; `ops store status` is unchanged from step 7.

## How criterion 9 is measured

Each sentence of the criterion has one reading. Every reading comes from the
running system, not from the deploy's intent.

| Criterion sentence | Reading |
| --- | --- |
| One process per data guest | `ops store status`: three FoundationDB processes, three zones, three machines, and each process address is a data guest's pinned address. |
| Mode double | `ops store status`: `Redundancy mode - double`. |
| Three coordinators | `ops store status`: three lines under `Coordination servers`, each a data guest's pinned address. |
| One backup agent per data guest | `docker ps` on each data guest shows `tack-fdb-backup-agent-1` running, and `ops store status` reports the backup session restorable. |
| Every client's cluster file writable | `docker inspect` on the app, the audit consumer, the backup agent, and the ops sidecar reports `rw=true` for `/etc/foundationdb`. |
| Every client's cluster file lists the real coordinators | `cat /etc/foundationdb/fdb.cluster` on all four guests matches the coordinator list in `ops store status`. |
| A coordinator change reaches each client file without a restart | Record each container's start time, run `ops store set-coordinators` with the three addresses in a different order, then read the four cluster files and the start times again. The files change; the start times do not. |
| Killing a data guest leaves reads and writes working | Stop a data guest. Poll `/healthz` and run a product write and read through the MCP interface once a second for two minutes. No window of non-200 answers longer than 10 seconds, and a write acknowledged before the stop reads back after it. |
| The restorable point still advances | `ops backup staleness-check` before the kill and 15 minutes after it: the FoundationDB restorable point is newer the second time. |

Run the kill three times, once per data guest, because the coordinator quorum
and the data placement are not symmetric until every guest has been the one
that died.

## The one step to rehearse first

Step 3 is the step with no undo. If the address named there is wrong or
unreachable, every client's cluster file is rewritten to an address that
answers nothing, and the store is unreachable until an operator edits four
files by hand. Rehearse it on a throwaway cluster on the QA owner guest before
running it against QA's store: start two processes from a scratch volume with
the same script and the same environment, move the coordinator between them,
and confirm the cluster file follows.
