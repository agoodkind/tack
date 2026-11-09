# Backup and resilience acceptance criteria

These requirements apply to the complete backup and resilience workstream.
Acceptance requires each measurement on QA before direct production observation.
Acceptance observations must use workstation probes, hypervisor readings, and object-store listings outside the guest under test. A configuration file cannot prove deployment or acceptance.

## 1. Continuous protection

- Two consecutive scheduled ledger exports must appear in object storage without
  an operator invocation. Run-key spacing must match the configured cadence
  within ten percent.
- Samples at the beginning and end of 24 unattended hours must show an advancing
  FoundationDB restorable point.
- Protection must not depend on an operator remembering a manual step.

## 2. Restores from object storage

- A scheduled rehearsal must rebuild both stores in temporary containers from
  object-store artifacts. Authentication tables must contain rows, a product
  range read must return keys, and the restored audit chain must verify.
- Temporary engines and dump tools must use the same engine images as the live
  stores. A successful restore on another version does not satisfy this test.
- Every restored-ledger read must use a login with only the required reader base
  role. A superuser read cannot establish application access.
- The age of the most recent passing rehearsal must not exceed eight days.

## 3. Single-guest loss

Each data guest must pass a QA failure rehearsal in turn. The rehearsal must
measure every service on that guest, including its product store and broker. The failure observations must meet the recovery limits.

Failure bounds:

| Measurement | Required result |
| --- | --- |
| External HTTP health probes | Each interval with non-200 responses lasts at most ten seconds. |
| Acknowledged pre-failure write | A read after the failure returns the acknowledged value. |
| Ledger leader election | Election finishes in less than five seconds. |
| Replication after the guest returns | Every cluster reports full replication within fifteen minutes. |

## 4. Recovery after data loss

A product-store restore against a known sequence of timestamped writes must lose
at most the last few seconds of writes. Ledger exports provide the off-host
recovery tier; the documented export cadence and completion lag determine its
recovery point. Quorum replication protects ledger availability between exports.

## 5. Staleness alarms


Each mechanism must pass a silent-staleness test with the following sequence:

1. The test must pause protection without producing a job error.
2. The test must observe the alarm within the mechanism's threshold plus fifteen minutes.
3. The test must resume protection and observe the fresh state.

The test must cover these mechanisms:

- The product restorable point.
- Completed export.
- Passing rehearsal.
- Full replication.

The check interval plus its randomized delay must fit within fifteen minutes.
- An alarm must report a fault that automatic recovery did not resolve. Export
  and rehearsal jobs must retry failures automatically within the day. Service containers
  must restart after failure. Object-store volume allocation must allow growth
  while free disk remains, rather than stop at a fixed volume-count ceiling.
- Each fault must produce one email on the transition to stale. Continued
  staleness and recovery must not produce additional messages. A message must
  state what stopped, when success last occurred, and what to check. It must
  report unreadable evidence as unreadable.
- Alarm mail must omit internal metric identifiers, thresholds expressed in
  seconds, run identifiers, object-store endpoints, and credentials.

## 6. Backup capture and placement

Backup capture must use native snapshot files rather than read the dataset row
by row. The export must collect required tablet snapshot files according to
tablet leadership. Export orchestration and rehearsals must run on a guest that does not
serve application traffic or database requests. Serving data guests may archive
their own local snapshot files. Unit installation and observed executions must
prove that placement.

## 7. Bounded disk use


Every guest must enable its daily prune timer. Disk-use evidence must compare consecutive deployments with each guest's baseline.

Disk-use criteria:

| Measurement | Required result |
| --- | --- |
| Deployment population | Five consecutive deployments. |
| Disk use on each guest | Returns to within five percent of its pre-deployment baseline. |
| Persisted database volumes | Pruning must preserve them. |


Hypervisor observations must confirm the required storage placement.

Storage placement criteria:

| Workload | Required tier |
| --- | --- |
| Ledger roots, product data, and app guests | NVMe. |
| Object storage, rehearsal scratch, export staging, and hypervisor guest backups | SATA SSD. |

- Bulk copying must not run without throttling on the ledger's storage pool.
  Production export and drill observations must show neither slow ledger sync
  nor leader elections during their execution windows.

## 8. Direct production evidence

Production observations must establish the required owner, app, and three data
guest roles. The owner requirement excludes serving traffic. The data guests
must provide the ledger, product store, and broker roles as their distribution
work completes. Evidence from the running system must establish each condition:

- Alarms are active.
- Timers are active.
- Exports are advancing.
- Replication is complete.
- Pruning is active.

An approved ledger migration requires the following order:

1. The operator must prepare the new guests.
2. The operator must join the three ledger nodes to the existing cluster.
3. The operator must wait for full replication.
4. The operator must repoint consumers.
5. The operator must retire the original node.

The migration must never create a second database universe. Each step requires operator confirmation during that migration. Backup-unit activation depends on consumers using the data tier.

## 9. Product store distributed

- Cluster status must show one FoundationDB process and backup agent per data
  guest, `double` redundancy, and three coordinators with one per data guest.
- Every client's cluster file must permit client writes and contain the actual
  coordinators. A coordinator change must update every client file without a
  restart.
- Loss of each data guest in turn must permit public product reads and writes.
  Each interval with non-200 responses must last at most ten seconds. The
  restorable point must keep advancing during the failure.

## 10. Event queue distributed

Broker metadata must establish the required topology and replication.

Event queue criteria:

| Measurement | Required result |
| --- | --- |
| Brokers | Three. |
| Audit topic | Three copies. |
| Consumer-position topic | Three copies. |
| Write requirement | Two live copies. |
| Each broker | A routable advertised address. |

Loss of any broker must still permit state changes and ledger consumption. Counts of events produced during the failure must equal the corresponding permanent ledger row counts.

## 11. App tier distributed

Proxy state must show both app guests, datastore health probes for each, and
selection of healthy instances only. An external probe during loss of an app
guest must show each interval with non-200 responses lasting at most ten seconds.

## 12. Request-path measurements

A QA trace of an authenticated read must establish the request-path budgets.

Authenticated-read budgets:

| Operation | Limit |
| --- | --- |
| Ledger writes | Zero. |
| Ledger reads | At most one. |
| Synchronous broker round trips | Zero. |

A state-change trace must show the audit event in the same product-store transaction as the change. Required durable read evidence still applies; this performance requirement does not permit unaudited reads.

Measurements must include cold and warm requests. Each instance reads current
token and membership data on the next lookup after its cache entry expires.
Expiry does not start a background refresh. A warm cache can conceal database
reads that a cold request requires. The token lookup and organization lookup
share one query on a cold request.

Token-use measurements must check stored timestamps and read latency under the required conditions.

Token-use criteria:

| Measurement | Required result |
| --- | --- |
| Stored token-use time | The stored token-use time must match a call within the flush interval. |
| Token use by two app guests within the same flush interval | Storage must retain the later time. |
| Read during a ledger-write pause lasting one flush interval | The read must return HTTP 200 within its normal latency. |

A single-hypervisor installation requires backup restoration after hypervisor
loss. These requirements do not add a second hypervisor for automatic failover.
