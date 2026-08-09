# Backup rearchitecture phase 3: ledger database on three nodes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task by task. Steps use checkbox syntax for tracking.

**Goal:** The audit ledger survives losing any one guest, and its export to the
object store runs on a schedule with no operator involved.

**Architecture:** The ledger database, YugabyteDB, grows from one node to three.
Each node runs on its own guest, and every row is stored three times. When the
node leading a piece of data dies, the surviving two elect a new leader in about
three seconds and writes continue. A scheduled export collects engine-native
snapshot files from all three nodes and writes them to the object store. Two
alarms report seconds since last success, one for the export and one for
replication health.

**Tech stack:** YugabyteDB, Docker Compose, Ansible, systemd timers, Go for the
export and the alarm metrics, SeaweedFS as the object store.

## Global constraints

- Every phase lands on the testbed first. Production waits for an explicit
  operator decision.
- Join the second and third nodes back to back. The intermediate two-node state
  has a fragile coordination quorum, so it must not persist.
- Each node declares explicit memory limits. The default self-sizing lets the
  database claim most of a guest's memory and starve everything else.
- Every Go slice passes three gates locally: `make build` complete with every
  check printing ok, `make test-unit`, and `make test-integration` against the
  local Docker cluster.
- Every input value is declared in the service's group file, the service
  inventory, or OpenTofu. No value is inferred from whether it was set.
- The export runs on a node that does not lead, so it never competes with the
  serving path.

## Prerequisites

The four testbed guests exist and carry Docker, the neighbor discovery proxy,
and a daily prune timer. Host preparation runs on every tack guest, and the
one-time provisioning work runs only on the guest that owns it. The ops sidecar
reaches the product store by naming the guest that serves it.

---

### Task 1: Give each ledger node its own compose service

**Files:**
- Modify: `docker-compose.yml` in the tack repository
- Modify: `ansible/inventory/group_vars/tack_all.yml` in the configs repository

**Interfaces:**
- Produces: one ledger service per data guest, each with a declared location, an
  explicit memory limit, and the patched startup script the single node already
  mounts. Task 2 consumes those services when it joins them.

- [ ] **Step 1: Read how the single node starts today**

Read the ledger service in `docker-compose.yml` and the startup script it
mounts. Record the flags it passes, the volume it persists, and the ports it
publishes. Do not change behavior in this step.

- [ ] **Step 2: Parameterize the service by guest**

The compose file gains the node's advertised address, its declared location,
and its memory limits as environment values, so one file serves every data
guest. The rendered environment file supplies them per guest.

- [ ] **Step 3: Declare the values per guest**

Each data guest's own group file declares its node index. The environment
group declares the memory limits, which are identical across guests within an
environment and smaller on the testbed than in production.

- [ ] **Step 4: Verify nothing changed for the existing node**

Deploy to the testbed owner guest. The ledger container restarts with the same
data volume, and a query returns the same row count as before the change.
Record the count before and after.

---

### Task 2: Join the second and third nodes

**Files:**
- Modify: `ansible/playbooks/deploy-tack.yml` in the configs repository

**Interfaces:**
- Consumes: the per-guest ledger services from Task 1.
- Produces: a three-node cluster holding three copies of every row. Task 3
  consumes the multi-node connection string.

- [ ] **Step 1: Bring both new nodes up back to back**

The deploy starts the ledger service on the second and third data guests in one
run, never leaving the cluster at two nodes between runs.

- [ ] **Step 2: Raise the copy count to three**

Set the cluster's replication factor to three once the third node has joined.

- [ ] **Step 3: Verify from outside the cluster**

From the workstation, read the cluster's own status and confirm three live
nodes, replication factor three, and no under-replicated data. Record the
output.

---

### Task 3: Point the app at all three nodes

**Files:**
- Modify: `tack/tack.env.j2` in the configs repository
- Modify: `ansible/inventory/group_vars/tack_prod_all.yml` and
  `tack_qa_all.yml` in the configs repository

**Interfaces:**
- Consumes: the three-node cluster from Task 2.
- Produces: connection strings listing all three nodes.

- [ ] **Step 1: Build the three-node connection string**

Each environment declares its three data guest addresses. The connection
strings for the app and the ops sidecar list all three. The existing database
driver supports this with no code change.

- [ ] **Step 2: Verify a node loss does not break the app**

Stop the ledger container on one data guest. From the workstation, confirm the
app still answers and a write still commits. Restart the container and confirm
the cluster reports full replication within fifteen minutes.

---

### Task 4: Rewrite the export for three nodes

**Files:**
- Modify: the ledger export implementation in the tack repository
- Create: an integration test covering a three-node archive

**Interfaces:**
- Consumes: the three-node cluster.
- Produces: an export that collects snapshot files from every node according to
  which node leads each piece of data. Task 5 schedules it.

- [ ] **Step 1: Establish that the current export is invalid at three nodes**

The current implementation archives from a single service container. Reproduce
that limitation against the three-node testbed cluster and record what it
misses. Stop and report if the premise does not hold.

- [ ] **Step 2: Collect from every node by leadership**

The archive phase walks the nodes and collects each node's snapshot files for
the data it leads.

- [ ] **Step 3: Prove a restore from the archive alone**

Restore the archive into a throwaway container and confirm the auth tables hold
rows and the audit chain verifies.

---

### Task 5: Run the export on a schedule

**Files:**
- Create: `common/services/tack-ledger-export.service` and
  `common/timers/tack-ledger-export.timer` in the configs repository
- Create: `ansible/playbooks/tasks/ledger-export.yml` in the configs repository
- Modify: `ansible/playbooks/deploy-tack.yml` in the configs repository

**Interfaces:**
- Consumes: the rewritten export from Task 4.
- Produces: two consecutive exports in the object store with no operator
  command, which is the first acceptance criterion.

- [ ] **Step 1: Install the unit and timer following the existing idiom**

Copy both units, register each result, and compute the reload from those flags.
Enable the timer, not the service. The repository already ships one service and
timer pair this way.

- [ ] **Step 2: Run it on a guest that does not lead**

The unit installs only on a data guest that is not leading, so the export never
competes with the serving path.

- [ ] **Step 3: Verify two unattended runs**

List the object store from the workstation and confirm two consecutive export
artifacts appear with no operator command, spaced within ten percent of the
configured cadence.

---

### Task 6: Report staleness for the export and for replication

**Files:**
- Modify: the metrics implementation in the tack repository
- Create: unit tests for both metrics

**Interfaces:**
- Consumes: the scheduled export and the cluster's replication state.
- Produces: two numbers, seconds since the last successful export and seconds
  since the cluster last reported full replication.

- [ ] **Step 1: Emit seconds since last success**

Each mechanism reports one number. An alert fires when that number ages past
its threshold, so a silently broken mechanism cannot stay dark.

- [ ] **Step 2: Prove the alarm fires on silence, not on error**

Pause the export so nothing errors. The number ages, and the alarm fires within
its threshold plus fifteen minutes. Resume, and it clears. Record both
transitions.

---

### Task 7: Production rollout

**Files:** none; this task runs commands.

- [ ] **Step 1: Confirm the testbed evidence**

Every measurement above passes on the testbed, recorded with its output.

- [ ] **Step 2: Ask the operator**

Production applies and deploys wait for an explicit decision, asked through the
harness question tool, naming exactly what will run.
