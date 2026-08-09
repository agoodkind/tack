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

- [ ] **Step 1: Give each node a permanent name every guest resolves**

The node announces the identity the engine stores in its internal catalog.
Today that is the container name `yugabyte`, which resolves only inside one
guest's container network, so three nodes on three guests cannot find each
other. Announcing an address instead would bake a value that can move into
the catalog, which is the wedge class the single-node name was chosen to
prevent.

Each node announces its own permanent name: `yb1`, `yb2`, or `yb3`.

The startup script refuses to start when the bound address and the announced
address differ, so a node binds exactly what it announces. The name a node
announces must therefore resolve, inside that container, to an address the
container can bind.

Each name resolves differently depending on which container asks. A node's
own name resolves to its own container address, which the container runtime
maps automatically once the container carries that name. The other two names
resolve to the peer guests' pinned addresses, written into the container's
own hosts file from values the rendered environment file supplies. Publish
the node ports peers need to reach.

Giving the ledger container the guest's own network would also let it bind
the guest address, and is rejected: it breaks the isolated address-per-guest
bridge these services run on, and exposes every node port on the guest
rather than only the ones peers need.

A container does not inherit the guest's name resolution, measured on the
testbed: a name added to a guest's hosts file resolved on that guest and
did not resolve inside a container on it. Writing the names on the guest
alone leaves the nodes unable to find each other.

Extend the service comment that forbids advertising an address so it states
the full contract: identity is a permanent name, addresses stay behind the
hosts entries, and a renumber changes one inventory line and a redeploy.

- [ ] **Step 2: Prove two guests resolve and reach each other's node**

Before any join, confirm from one data guest that another node's name
resolves to that guest's pinned address and the published port answers. A
join attempted without that evidence creates a second cluster instead of
growing the first.

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

The target topology puts one ledger node on each data guest and none on an app
guest. The node running today lives on an app guest, so reaching that topology
means three new nodes join and the original retires, not two nodes joining a
survivor.

- [ ] **Step 1: Join all three data guest nodes back to back**

The deploy starts the ledger service on all three data guests in one run. The
cluster passes through four nodes, which is a stable state, and never rests at
two, whose coordination quorum is fragile.

- [ ] **Step 2: Raise the copy count to three**

Set the cluster's replication factor to three once all three have joined.

- [ ] **Step 3: Wait for every copy to land before retiring anything**

Confirm from outside the cluster that no data is under-replicated. Retiring the
original node before its data has copied elsewhere loses the only copy of
whatever it still leads alone.

- [ ] **Step 4: Retire the original node**

Remove the node on the app guest through the engine's own removal path, so the
remaining three re-replicate what it held. The app guest then carries no data,
which is what lets the export run somewhere that serves nothing.

- [ ] **Step 5: Verify the end state from outside the cluster**

Read the cluster's status and confirm three live nodes, all on data guests,
replication factor three, and no under-replicated data. Record the output.

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
