# Expand ledger storage to three guests

Implement each task with `superpowers:subagent-driven-development` or
`superpowers:executing-plans`. Track completion with the checkboxes.

Expand YugabyteDB from one node to three guests and store three copies of
every row. After a leader fails, the surviving two elect a replacement in
about three seconds and resume writes. Schedule engine-native snapshot exports
from all three nodes to SeaweedFS. Report seconds since the last successful
export and the last fully replicated state.

Use Docker Compose, Ansible, systemd timers, and Go for exports and metrics.

## Constraints and prerequisites

- Validate every phase on the testbed. Require explicit operator approval
  before production applies and deploys.
- Join the second and third nodes back to back. Do not leave a two-node
  cluster, which cannot tolerate a coordination-node failure.
- Set explicit database memory limits. Default self-sizing can consume most
  guest memory and prevent other services from operating.
- Require `make build` with every check printing `ok`, `make test-unit`, and
  `make test-integration` against the local Docker cluster for every Go change.
- Declare every input in the service group file, service inventory, or OpenTofu.
  Do not infer values from their presence.
- Run exports on a non-leading node rather than competing with request work.

Require the four testbed guests to have Docker, the neighbor discovery proxy,
and a daily prune timer. Run host preparation on every Tack guest and
provisioning only on its owner. Configure the ops sidecar to connect to the
product store using the serving guest's address.

## Task 1: Define ledger services

Modify `docker-compose.yml` in Tack and
`ansible/inventory/group_vars/tack_all.yml` in configs. Define one service per
data guest with a distinct location, explicit memory limits, and the existing
patched startup script. Task 2 joins these services.

- [ ] Give each node a permanent identity, `yb1`, `yb2`, or `yb3`.

The engine persists the announced identity in its catalog. The current identity,
`yugabyte`, resolves only inside one guest's container network. Announcing a
literal address would make the catalog identity stale after renumbering.

The startup script requires identical bind and announced addresses. Inside each
container, resolve its own identity to its container address and peer identities
to their pinned guest addresses. The runtime supplies self-resolution from the
container hostname. Testbed measurements confirm peer connections through
published node ports in routed gateway mode.

Render a per-guest compose override beside the base file. Include final values
rather than variables or shell commands: the service hostname, peer address
entries in `extra_hosts`, and the full replacement command. Compose replaces
command lists rather than merging them. Change only the announced identity and
preserve the flagfile argument verbatim. Omitting it restores the self-sizing
that caused the 2026-08-05 incident. Store resource bounds in one Tack flags
file, one flag per line, and mount it in the container.

Do not use guest networking for the ledger container. It removes address
isolation and exposes every node port rather than only required peer ports.
Testbed measurements also show that guest hosts-file entries do not configure
container resolution; write entries inside containers.

- [ ] Before joining, verify from one data guest that another node's identity
  resolves to the pinned guest address and its published port answers.
  Joining without this proof can create a second cluster.
- [ ] Declare each node index in its guest group file. Declare shared memory
  limits per environment, with smaller testbed limits than production limits.
- [ ] Deploy to the testbed owner. Record row counts before and after the
  existing ledger container restarts with its current data volume. Require
  identical counts.

## Task 2: Join three data nodes

Modify `ansible/playbooks/deploy-tack.yml` in configs. The existing node runs
on an app guest. Join all three new data nodes before retiring it, temporarily
running four nodes with three copies per row.

- [ ] Start ledger services on all three data guests in one deploy. Do not
  leave the cluster at two nodes.
- [ ] Set replication factor three after all three nodes join.
- [ ] Verify full replication from outside the cluster before retiring the
  original node. Early retirement can delete the only copy of data it leads.

## Task 3: Update consumers and retire the original node

Modify `tack/tack.env.j2`,
`ansible/inventory/group_vars/tack_prod_all.yml`, and `tack_qa_all.yml` in
configs. Update consumers before retirement because they currently resolve the
original node's identity.

- [ ] Declare the three data guest addresses per environment. List them in
  app and ops sidecar connection strings. The existing driver supports this
  without code changes.
- [ ] Stop one data guest's ledger container. From the workstation, verify
  that the app answers and a write commits. Restart the container and require
  full replication within fifteen minutes.
- [ ] Blacklist the original tablet server, wait for the engine's load-move
  metric to equal one hundred, and transfer its master role to a data node.
  Require the three data nodes to replicate its data before retirement. The
  app guest must stop storing data before it orchestrates exports.
- [ ] Record external status showing three live nodes on data guests,
  replication factor three, and full replication.

## Task 4: Export snapshots from all nodes

Modify the Tack ledger export implementation and add a real three-node archive
integration test. Each export must include all node snapshot files under one
run key and a completeness manifest.

Do not collect files with inter-guest SSH. That method gives the most exposed
guest SSH access to the data tier, and observed guest SSH configuration drifts.
Each node must archive its own files using existing guest mechanisms.

- [ ] Verify the current export's limitation against code. It dumps schema
  and archives tablets inside one local container. That captures at most one
  node's third of tablet files, and that container no longer runs on the
  export guest.
- [ ] Make the non-serving guest orchestrator create the cluster snapshot,
  export metadata, dump schema, and write a manifest of required node uploads.
  Make each data guest upload its own node's tablet snapshot files under the
  shared run key. Refuse restoration unless every manifest requirement matches
  the uploaded artifacts.
- [ ] Restore from the archive alone in a throwaway container. Require rows
  in the auth tables and successful audit-chain verification.

## Task 5: Schedule exports

Create `common/services/tack-ledger-export.service`,
`common/timers/tack-ledger-export.timer`, and
`ansible/playbooks/tasks/ledger-export.yml` in configs. Update
`ansible/playbooks/deploy-tack.yml` to install them.

- [ ] Copy the service and timer units, register their results, and compute
  systemd reload from those change flags. Enable the timer rather than the
  service, following the existing service-and-timer installation pattern.
- [ ] Install the orchestrator timer only on the non-serving guest. Install
  a local archive upload unit on each data guest. Limit serving-node backup
  work to that local archive.
- [ ] List object-store artifacts from the workstation. Require two consecutive
  unattended exports with spacing within ten percent of the configured cadence.

## Task 6: Record export and replication staleness

Modify Tack metrics and add unit tests for both metrics.

- [ ] Report seconds since the last successful export and the last fully
  replicated state. Alarm after each age exceeds its threshold, including
  when a job stops without reporting an error.
- [ ] Pause exports without causing an error. Require the alarm within its
  threshold plus fifteen minutes. Resume exports and require the alarm to
  clear. Record both transitions.

## Task 7: Approve production rollout

- [ ] Require every testbed measurement above to pass and record its output.
- [ ] Use the harness question tool to request operator approval for the
  exact production applies and deploys before running them.
