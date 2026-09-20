# Spread the event queue across the three data guests

The audit event queue runs one Apache Kafka broker on the owner guest, keeping
one copy of every partition, and losing that guest loses every event the
consumer has not yet projected. Five defects block any expansion. All five were
measured on production on 2026-09-20 from the broker's own metadata.

1. The KRaft quorum was formatted static. `kafka-features.sh describe` reports
   `kraft.version` at finalized level 0 with a supported maximum of 1, and
   `kafka-metadata-quorum.sh describe --status` reports
   `CurrentVoters: [{"id": 1, "endpoints": ["CONTROLLER://kafka:9093"]}]`.
   At level 0 the voter set comes from static configuration alone, and there is
   no online command that adds a voter.
2. The broker advertises the Docker service name `kafka`, which resolves inside
   one guest's bridge only. The stack file publishes no port for the service, so
   nothing on another guest can reach the broker at all.
3. `audit.events.v1` has 256 partitions at replication factor 1, and
   `__consumer_offsets` has 50 partitions at replication factor 1. A topic's
   copy count is fixed when the topic is created. Raising
   `KAFKA_DEFAULT_REPLICATION_FACTOR` or `KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR`
   changes neither of the two topics that already exist.
4. The consumer creates the audit topic with `ReplicationFactor = -1`, which
   asks the broker for its default. On a fresh three-broker cluster that default
   decides the copy count, and the stack file's default is 1. A rebuilt QA would
   come back with one copy of every partition.
5. A dynamic default broker configuration of `min.insync.replicas=1` is set
   cluster-wide. `kafka-configs.sh --describe --entity-type brokers
   --entity-name 1 --all` prints
   `min.insync.replicas=1 synonyms={DYNAMIC_DEFAULT_BROKER_CONFIG:min.insync.replicas=1, STATIC_BROKER_CONFIG:min.insync.replicas=1, DEFAULT_CONFIG:min.insync.replicas=1}`.
   The dynamic entry comes first in that list and therefore wins. Raising
   `KAFKA_MIN_INSYNC_REPLICAS` in the stack file, which is the static entry,
   would leave the effective minimum at one. The audit topic sets only
   `retention.ms` at the topic level.

Two further facts shape the order below.

Published ports cross guests on this network, and the product store's phase 4
used host networking for the same requirement a broker has: a process must bind
the address it advertises, and a container address is replaced on the next
recreate. A broker on a data guest therefore runs on host networking and
advertises that guest's pinned address from `service_mapping.yml`.

A partition reassignment never moves a committed offset. The consumer group's
positions are records inside `__consumer_offsets`, and a reassignment copies log
segments and adds replicas without renumbering anything. The consumer keeps
running through both reassignments below.

## What the code change does

Each defect has one fix, and none of them changes a running broker by itself.

| Defect | Fix |
| --- | --- |
| Static quorum with one voter | `docker-compose.yml` reads `TACK_QUEUE_NODE_ID` and `TACK_QUEUE_CONTROLLER_VOTERS`. The deploy renders the declared voter set on every guest. |
| The broker advertises a bridge-local name | `docker-compose.yml` reads `TACK_QUEUE_ADVERTISED_LISTENERS`. The deploy renders `PLAINTEXT://[<guest pinned address>]:9092` on a guest that runs a broker, and the override puts that broker on host networking. |
| A topic's copies are fixed at creation | `ops queue set-replication` submits one `AlterPartitionReassignments` request against a named topic, with an optional replication throttle. `ops queue replication-progress` reports how far it has run. |
| The consumer creates the topic at the broker's default | `ConsumerConfig` gains `TopicReplicationFactor` and `TopicMinInSyncReplicas`, read from `AUDIT_CONSUMER_TOPIC_REPLICATION_FACTOR` and `AUDIT_CONSUMER_TOPIC_MIN_INSYNC_REPLICAS`. Zero keeps today's behavior. |
| A dynamic default outranks the stack file | `ops queue set-min-insync` writes `min.insync.replicas` as a topic-level setting, which outranks every broker-level value. It refuses when the topic's smallest partition has fewer copies than the requested minimum. |

Three more changes come with them.

`ops queue status` is the criterion-10 reading. It prints every broker id with
the address that broker advertises, the controller quorum's leader and voters,
and for the audit topic and the consumer-position topic the partition count, the
smallest and largest replica count, the smallest in-sync count, and the
effective `min.insync.replicas`. Every number comes from the cluster's own
metadata rather than from what a deploy intended. The family runs through the
`app` compose service, not `tack-ops`: `tack-ops` is host-networked and cannot
resolve the bridge name. `ops audit dlq` already runs through `app` for that
same reason.

The stack file's health probe reads `TACK_QUEUE_PROBE`.
`kafka-broker-api-versions.sh` asks the cluster for every node and then connects
to each one at the address that node advertises. On three brokers that makes one
broker's health depend on its peers, and a guest loss would mark the two
survivors unhealthy and restart them during the failure the cluster exists to
absorb. A guest that runs a broker renders a probe that asks the local
controller alone.

On QA and production the deploy is a no-op until an operator changes the values
in the next section: `tack_queue_node_present` and `tack_queue_distributed` are
false everywhere, `tack_queue_legacy_node_present` is true, the copy counts are
1, and `tack_queue_audit_topic_min_insync` is 0. Every rendered value then
matches the value the stack file has always held.

## The blocking decision

Growing the quorum has two routes, and the ticket names both. The decision is
made on QA, on a throwaway cluster, before either is run against QA's queue.

Route A upgrades the quorum to dynamic. Run
`kafka-features.sh upgrade --feature kraft.version=1`, which converts the static
voter set into a dynamic one, then start each new controller and add it with
`kafka-metadata-quorum.sh add-controller`. The obstacle is the image: a
controller joining a dynamic quorum must have its storage formatted with
`--no-initial-controllers`, and the stock `apache/kafka` entrypoint formats
storage itself without that flag. Taking this route means formatting the two new
nodes' storage by hand before their first start.

Route B rebuilds the quorum as three static voters. Set
`TACK_QUEUE_CONTROLLER_VOTERS` to the three-entry list on all three guests, let
the two new nodes format their own storage from the shared `KAFKA_CLUSTER_ID`,
and restart node 1 with the new voter set. At `kraft.version` 0 the voter set is
read from configuration on every start. The two new controllers then replay node
1's metadata log and the cluster reaches three voters. This route needs no image
change and no hand-formatting, and it costs one restart of the single broker,
which is a maintenance window.

A third shape was considered and rejected: two new nodes as brokers only, with
the controller quorum left at one voter. That shape satisfies the copy counts
and fails the kill test, because losing the owner guest would take the metadata
plane with it. Criterion 10 says any one broker.

Route B is the recommended one, on the evidence above. Rehearse it on a
throwaway cluster on the QA owner guest first: three containers from scratch
volumes with the same image and the same environment, formatted from one cluster
id, and confirm all three report as voters.

## Migration order

Every step is run by an operator and its result is read before the next one
starts. Steps 1 through 3 leave the running broker untouched.

### 1. Deploy the code at the merged refs

The app and the audit consumer are recreated with the rendered queue variables.
Every value equals what the stack file already held.

Read: `docker compose run --rm app ops queue status` reports one broker,
`audit.events.v1` at 256 partitions with one replica each,
`__consumer_offsets` at 50 partitions with one replica each, and an effective
`min.insync.replicas` of 1 on both. The running broker's configuration is
unchanged.

This reading is also the first proof that the new command talks to the broker at
all.

### 2. Measure what a reassignment has to copy

```
docker exec tack-kafka-1 /opt/kafka/bin/kafka-log-dirs.sh \
  --bootstrap-server kafka:9092 --describe --topic-list audit.events.v1
```

The throttle in step 5 is chosen from this number and the guests' link, not from
a default. Record it.

Production's `audit.events.v1` measured 23,105,584 bytes across its 256
partitions on 2026-09-20. Three copies of that is under 50 MB, which crosses
the guests' link in seconds. The throttle exists for the case where QA or a
later production run measures a much larger topic; at this size the risk in
this plan is the quorum rebuild in step 4, not the copy in step 5.

### 3. Rehearse the quorum rebuild

On the QA owner guest, start three throwaway brokers from scratch volumes with
one shared cluster id and the three-entry static voter set, and confirm
`kafka-metadata-quorum.sh describe --status` reports three voters. Remove them.
This is the step that decides between route A and route B on evidence.

### 4. Start a broker on each data guest

Set in the three `tack_dataN_suburban_servers.yml` files:

```yaml
tack_queue_node_present: true
```

Set in `tack_qa_all.yml`:

```yaml
tack_queue_distributed: true
tack_queue_node_ids:
  tack-qa.suburban.goodkind.io: 1
  tack-data1.suburban.goodkind.io: 2
  tack-data2.suburban.goodkind.io: 3
  tack-data3.suburban.goodkind.io: 4
tack_queue_controller_voters:
  - "2@[3d06:bad:b01:210::220]:9093"
  - "3@[3d06:bad:b01:210::221]:9093"
  - "4@[3d06:bad:b01:210::222]:9093"
tack_queue_bootstrap_addresses:
  - "3d06:bad:b01:210::220"
  - "3d06:bad:b01:210::221"
  - "3d06:bad:b01:210::222"
```

The three new node ids are 2, 3, and 4, and the owner keeps 1. A KRaft node id
is permanent: the cluster records it, and a reused id would make two brokers
each treat the other's partitions as their own.

This is the maintenance window. Under route B the owner's broker restarts with a
voter set that no longer names it, and both topics are unavailable for the
length of that restart. The producer spills to the outbox for the duration, and
the consumer resumes from its committed position.

Deploy. Each data guest starts one broker on host networking.

Read: `ops queue status` reports four brokers, each at its own advertised
address, and three voters in the controller quorum. Both topics still hold one
copy of every partition.

### 5. Raise the copies on the audit topic

```
docker compose run --rm app ops queue set-replication \
  --topic audit.events.v1 --replicas 3 --throttle-bytes <from step 2> --execute
docker compose run --rm app ops queue replication-progress --topic audit.events.v1
```

Poll the second command until no partition is still moving.

Read: `ops queue status` reports `audit.events.v1` with three replicas and three
in-sync replicas on every partition.

### 6. Raise the copies on the consumer-position topic

```
docker compose run --rm app ops queue set-replication \
  --topic __consumer_offsets --replicas 3 --throttle-bytes <from step 2> --execute
docker compose run --rm app ops queue replication-progress --topic __consumer_offsets
```

Moving a `__consumer_offsets` partition moves a group coordinator, and the
consumer rejoins its group when that happens. Its committed positions are
records inside the partitions being copied and are not renumbered.

Read: `ops queue status` reports `__consumer_offsets` with three replicas and
three in-sync replicas on every partition, and the consumer's log shows it
rejoined rather than restarted from the topic's beginning.

### 7. Clear the throttles

```
docker compose run --rm app ops queue clear-throttle --topic audit.events.v1 --execute
docker compose run --rm app ops queue clear-throttle --topic __consumer_offsets --execute
```

A throttle left behind slows every later replication, including the catch-up
after a guest comes back.

### 8. Require two live copies

```
docker compose run --rm app ops queue set-min-insync --topic audit.events.v1 --replicas 2 --execute
docker compose run --rm app ops queue set-min-insync --topic __consumer_offsets --replicas 2 --execute
```

The command refuses while a partition has fewer copies than the requested
minimum, which is the guard against ordering this before step 6.

A topic-level setting outranks the cluster-wide dynamic default of 1 that
defect 5 names, and a topic created after this point would still inherit that
1. Delete the dynamic default in the same window:

```
docker exec tack-kafka-1 /opt/kafka/bin/kafka-configs.sh \
  --bootstrap-server kafka:9092 --alter --entity-type brokers --entity-default \
  --delete-config min.insync.replicas
```

The static value from `TACK_QUEUE_MIN_INSYNC_REPLICAS` then applies to every
topic created from here on.

Set in `tack_qa_all.yml`:

```yaml
tack_queue_replication_factor: 3
tack_queue_min_insync_replicas: 2
tack_queue_audit_topic_min_insync: 2
```

Deploy. The three values describe the end state. A QA rebuild from empty needs
that end state.

Read: `ops queue status` reports an effective `min.insync.replicas` of 2 on both
topics, and a product write through the MCP interface still commits.

### 9. Retire the broker on the owner guest

Set in `tack_qa_all.yml`:

```yaml
tack_queue_legacy_node_present: false
```

Also set `tack_queue_node_ids` to the three data guests alone and remove the
owner's entry.

Deploy. The override parks `kafka` in the retired profile on the owner guest and
the deploy removes its container.

Read: `docker ps` on the owner guest shows no `tack-kafka-1`; `ops queue status`
reports three brokers, three voters, and both topics unchanged from step 8.

## How criterion 10 is measured

Each sentence of the criterion has one reading. Every reading comes from the
brokers' own metadata.

| Criterion sentence | Reading |
| --- | --- |
| Three brokers | `ops queue status`: three broker ids, and each advertised address is a data guest's pinned address. |
| The audit topic at three copies | `ops queue status`: `audit.events.v1` smallest and largest replica count both 3. |
| The consumer-position topic at three copies | `ops queue status`: `__consumer_offsets` smallest and largest replica count both 3. |
| Writes requiring two live copies | `ops queue status`: effective `min.insync.replicas` 2 on both topics, read from the cluster's config API rather than from the stack file. |
| Each broker advertising its own routable address | `ops queue status`: no two brokers share an advertised host, and each host answers a TCP connect from another guest. |
| Read from the brokers' own metadata | Every number above comes from one `ops queue status` run, which issues Metadata, DescribeConfigs, and DescribeQuorum requests. |
| Killing any broker leaves state-change events committing | Stop a data guest. Run a product write through the MCP interface once a second for two minutes and count the acknowledged writes. No window of refused writes longer than 10 seconds. |
| The consumer keeps writing | During the same kill, `ops queue replication-progress` and the consumer's own `consumer.processed` log lines show it advancing; its committed offset moves. |
| Events produced during the kill equal the ledger rows for them | Count the events the producer acknowledged during the kill window, then count `audit.events` rows in the same window through `audit query`. The two counts are equal, and the chain verifies with `audit verify`. |

Run the kill three times, once per data guest, because the controller quorum and
the partition leadership are not symmetric until every guest has been the one
that died.

Two readings belong to the deploy rather than the cluster. `docker inspect` on
each data guest's `tack-kafka-1` reports host networking, and the guest's `.env`
sets `TACK_QUEUE_ADVERTISED_LISTENERS` to that guest's own pinned address.

## The one step to rehearse first

Step 4 is the step with no undo. Under route B the owner's broker restarts with
a voter set that no longer names it, and if the two new controllers cannot
assemble a quorum the cluster has no metadata plane and neither topic can be
read or written. Both topics then hold one copy each, on the owner guest's disk,
with nothing serving them. Rehearse the quorum rebuild on a throwaway cluster
first, which is step 3, and take the QA step only after that rehearsal reports
three voters.
