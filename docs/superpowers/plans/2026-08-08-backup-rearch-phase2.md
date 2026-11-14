# Provision backup resilience guests in phase 2

Use `superpowers:subagent-driven-development` or `superpowers:executing-plans`
to implement these tasks and track their checkboxes. Follow the
[backup design](../specs/2026-08-06-backup-rearch-design.md).

This phase changes `/Users/agoodkind/Sites/configs`: guest mapping, OpenTofu
resources, guest identity, deployment preparation, routable environment
addresses, and daily Docker pruning. Add three data guests and one app guest
per environment, with QA IDs equal to production plus 100. Keep current
services on the owner until phases 3 through 5 join the new cluster members.
The original signing-key vault proposal in TACK-353 does not proceed; Task 6
preserves the operator's per-guest-key decision.

Use OpenTofu's bpg/proxmox provider, Ansible through
`go run goodkind.io/configs/cmd/configs`, systemd, and ansible-vault.
TACK-350 tracks guests, TACK-351 deployment separation, TACK-352 addresses,
and TACK-432 pruning.

## Constraints

- Validate on QA before production. Run one environment per command.
- Declare guest IDs and addresses only in
  `ansible/inventory/group_vars/all/service_mapping.yml`. Read command values
  from it rather than hardcoding them. A hardcoded ID previously targeted
  production during a testbed deploy.
- Run OpenTofu only through `go run goodkind.io/configs/cmd/configs tofu <command>`
  for vault-supplied backend and provider credentials. Target
  `-target=module.suburban` for QA and `-target=module.vault` for production.
  Untargeted runs include production.
- Run deploys through `go run goodkind.io/configs/cmd/configs deploy <playbook> [--limit <group>]`,
  not `ansible-playbook`. Review `--check --diff` before production mutation.
- Fetch origin and fast-forward local main before deploying at `origin/main`.
- Require explicit approval for production apply, deploy, or debianct destruction.
  QA work does not require that approval.
- Keep secrets out of output. Only the operator generates and vaults a proposed
  signing key; agents use its variable reference.
- Require `go run goodkind.io/configs/cmd/configs lint` before Ansible commits
  and `tofu validate` before OpenTofu commits.
- Declare inputs in group_vars, service mapping, or OpenTofu. Do not infer
  values from presence. Prohibit `default()`, `is defined`, `.get(key, default)`,
  `| length` presence tests, and `lookup(..., default=)` without exemptions.
  Lint before every deploy.
- Match group variable filenames exactly to `<group>_servers.yml`. Ansible
  silently ignores a mismatched filename.
- Use distinct Proxmox hostnames across hypervisors. Identical names merge
  inventory hosts and allow the later plugin to overwrite attributes.
- Use mapping-driven static addresses. A pinned MAC establishes link identity,
  not address reservation.
- Keep YAML under 90 columns, with a hard maximum of 120.
- Sign commits with imperative subjects and
  `Co-authored-by: Claude <noreply@anthropic.com>`.

## Assign guest identities and capacity

Use these identities after confirming that production IDs and their QA
counterparts do not collide:

| Key | Hostname | VMID | IPv6 | Docker /96 | MAC |
|---|---|---|---|---|---|
| `tack_data1` | tack-data1.home.goodkind.io | 120 | `3d06:bad:b01::120` | `3d06:bad:b01:0:7ad::/96` | `BC:24:11:A3:52:20` |
| `tack_data2` | tack-data2.home.goodkind.io | 121 | `3d06:bad:b01::121` | `3d06:bad:b01:0:7ae::/96` | `BC:24:11:A3:52:21` |
| `tack_data3` | tack-data3.home.goodkind.io | 122 | `3d06:bad:b01::122` | `3d06:bad:b01:0:7af::/96` | `BC:24:11:A3:52:22` |
| `tack_app2` | tack-app2.home.goodkind.io | 123 | `3d06:bad:b01::123` | `3d06:bad:b01:0:7b0::/96` | `BC:24:11:A3:52:23` |
| `tack_data1_suburban` | tack-data1.suburban.goodkind.io | 220 | `3d06:bad:b01:210::220` | `3d06:bad:b01:210:7ad::/96` | `BC:24:11:04:01:20` |
| `tack_data2_suburban` | tack-data2.suburban.goodkind.io | 221 | `3d06:bad:b01:210::221` | `3d06:bad:b01:210:7ae::/96` | `BC:24:11:04:01:21` |
| `tack_data3_suburban` | tack-data3.suburban.goodkind.io | 222 | `3d06:bad:b01:210::222` | `3d06:bad:b01:210:7af::/96` | `BC:24:11:04:01:22` |
| `tack_app2_suburban` | tack-app2.suburban.goodkind.io | 223 | `3d06:bad:b01:210::223` | `3d06:bad:b01:210:7b0::/96` | `BC:24:11:04:01:23` |

Use the [capacity decision](2026-08-08-phase2-capacity.md): QA data guests use
2 cores, 4096 MB, and 40 GB disks; its app guest uses 2 cores, 2048 MB, and
30 GB. Production data guests use 4 cores, 16384 MB, and 60 GB; its app guest
uses 4 cores, 8192 MB, and 40 GB. These memory ceilings do not reserve RAM,
and phase 2 does not start compose stacks on new guests. Before phase 3,
require decisions to reclaim debianct and shrink guest 117 as its data
services stop. Guest 117 remains app instance one and serves the full stack
until the later data migrations.

## Task 1: Measure and approve capacity

Create the capacity record in Tack alongside execution evidence. It must
record physical RAM, configured guest memory before and after this phase,
free datastore space against proposed disks, and a `fits` or
`requires debianct reclaim` verdict for each hypervisor. The original proposal
required 360 GB on production `local-lvm` and 150 GB on QA `local-zfs`.

- [ ] Read vault directly:

```bash
ssh root@vault.home.goodkind.io 'free -g; pvesm status; pct list'
```

If DNS fails, read the `vault` address from configs `ansible/inventory/hosts`.
Read suburban's address from that inventory and inspect it:

```bash
ssh root@<suburban-address-from-inventory> 'free -g; pvesm status; pct list'
```

- [ ] Write measurements and verdicts. Stop and request confirmation before
  destroying debianct, VMID 100 with 8 cores and 16 GB, even though the design
  permits reclamation after approval.
- [ ] Commit:

```bash
git add docs/superpowers/plans/2026-08-08-phase2-capacity.md
git commit -S -m "Record hypervisor capacity reading for phase 2 sizing

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 2: Map guests and inventory groups

Modify service entries near lines 62 to 85 and `group_children` near line 214
in `ansible/inventory/group_vars/all/service_mapping.yml`.

- [ ] Inspect the inventory:

```bash
cd /Users/agoodkind/Sites/configs
go run goodkind.io/configs/cmd/configs inventory-dump > /tmp/inv.json
```

Read `groups` and `keyed_groups` in `ansible/inventory/vault.proxmox.yml` and
`suburban.proxmox.yml`. Determine whether tags or hostname patterns create
`tack_servers` and `tack_qa_suburban_servers`. Apply matching metadata to create
`tack_data_servers`, `tack_app_servers`, `tack_qa_data_suburban_servers`, and
`tack_qa_app_suburban_servers`. Include guest 117 among app guests if supported.
Record the grouping mechanism in the commit.

- [ ] Add production entries after `tack:` and QA entries after
  `tack_qa_suburban:`, preserving the existing `hostname`, `vmid`, and `ipv6`
  structure and exact identity table values.
- [ ] Extend groups:

```yaml
  tack_all:
    - tack_servers
    - tack_qa_suburban_servers
    - tack_data_servers
    - tack_qa_data_suburban_servers
    - tack_app_servers
    - tack_qa_app_suburban_servers
  tack_data_all:
    - tack_data_servers
    - tack_qa_data_suburban_servers
```

- [ ] Lint and commit:

```bash
go run goodkind.io/configs/cmd/configs lint
git add ansible/inventory/group_vars/all/service_mapping.yml
git commit -S -m "Map the phase 2 tack data and app guests with cluster groups

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 3: Create OpenTofu resources

Create Debian 13 LXC resources in `opentofu/vault/tack_data.tf` for production
and `opentofu/suburban/tack_data_qa.tf` for QA. Require Task 1's `fits` verdict
or completed reclamation and Task 2's mappings before apply.

- [ ] Copy the resource structure from `opentofu/vault/containers.tf:1-66`.
  Change resource identity, mapping-driven `vm_id`, hostname, address, MAC,
  and sizes. Use `local.service_mapping.tack_data1.vmid` and equivalent lookups
  rather than copying existing hardcoded addresses. Use tags
  `["lxc", "tack", "tack-data", "docker"]` or
  `["lxc", "tack", "tack-app", "docker"]`, adjusted for Task 2's grouping.
  Preserve `prevent_destroy = true` and the existing `ignore_changes` list.
- [ ] Copy QA structure from `opentofu/suburban/tack_qa.tf`, preserving
  mapping-driven addresses, `local-zfs`, the trunk bridge, and omission of
  `start_on_boot`. Apply the table identities and QA sizes.
- [ ] Initialize and plan from the configs root:

```bash
cd /Users/agoodkind/Sites/configs
go run goodkind.io/configs/cmd/configs tofu init
go run goodkind.io/configs/cmd/configs tofu plan -target=module.suburban
```

`opentofu/` defines one root with `module.suburban` and `module.vault`.
Environment subdirectories do not configure backends or providers. State uses
Cloudflare R2 and vault credentials through the configs command. Do not invoke
`tofu` directly or fall back to untargeted operations.
Require exactly `4 to add, 0 to change, 0 to destroy`. Stop and report any
changed or destroyed resource. Task 9 uses the equivalent production target.

- [ ] Commit, then apply only QA and require SSH access at each mapping address:

```bash
git add opentofu/vault/tack_data.tf opentofu/suburban/tack_data_qa.tf
git commit -S -m "Declare the phase 2 tack data and app guest containers

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 4: Declare guest networking and roles

Use mapping entries for networking and per-guest group files for roles.
`host_vars` does not satisfy the input declaration rule; the existing file
only corrects inventory precedence.

- [ ] Add `docker_v6_subnet` and `docker_v6_gateway` to each Tack mapping entry.
  Read existing guest 117 and 217 values from environment group files before
  removing those declarations. Assign each new guest the table's distinct /96
  and gateway at prefix plus `::1`. Read subnet values from the mapping in
  environment group files.
- [ ] Create ten `<mapping-key>_servers.yml` files under
  `ansible/inventory/group_vars/`. Each file sets only `tack_cluster_role`
  (`data` or `app`) and `tack_provision_owner` (`true` for exactly one guest per
  environment and `false` elsewhere). Match each guest group exactly.
- [ ] Render, lint, and commit:

```bash
go run goodkind.io/configs/cmd/configs deploy deploy-tack --limit tack_qa_suburban_servers --check --diff
go run goodkind.io/configs/cmd/configs lint
git add ansible/inventory
git commit -S -m "Declare tack guest Docker subnets in the mapping and per-guest roles in group vars

Co-authored-by: Claude <noreply@anthropic.com>"
```

Require zero rendered changes on guest 217; this task relocates declarations
without changing their values.

## Task 5: Separate preparation from provisioning

Modify `ansible/playbooks/deploy-tack.yml` for TACK-351.

- [ ] Change the import target at lines 15 to 18 and main `hosts:` at
  lines 20 to 21 from `tack_servers:tack_qa_suburban_servers` to `tack_all`.
  Run apt, Docker, sysctl, ndppd, and daemon.json preparation on all guests.
- [ ] Put one-time tasks in a second `hosts: tack_all` play gated by
  `when: tack_provision_owner`: `/etc/tack` creation at 125, signing key at
  133, `/etc/foundationdb` at 152, and provision at 269. Preserve migrations,
  gated FDB configuration, audit roles, and seed. Keep Compose fetch and startup
  owner-only until phases 3 through 5 assign services to the new guests.
- [ ] Render both environments, lint, and commit:

```bash
go run goodkind.io/configs/cmd/configs deploy deploy-tack --limit tack_qa_suburban_servers --check --diff
go run goodkind.io/configs/cmd/configs lint
git add ansible/playbooks/deploy-tack.yml
git commit -S -m "Split deploy-tack into all-guest prep and provision-owner plays

Co-authored-by: Claude <noreply@anthropic.com>"
```

Deploy the QA data group. Require Docker on all three new data guests without
compose stacks. Verify unchanged `docker ps` output and `/healthz` 200 on guest 217.

## Task 6: Preserve per-guest signing keys

The operator canceled vault sharing on 2026-08-08. This phase must not change
key creation. Notarization rows currently store a signer-supplied public-key
hash without an accepted signer list. An attacker with app guest access could
generate a key and forge a checkpoint indistinguishable from a new legitimate
guest. A shared key would simplify rotation but would not prevent this attack.

TACK-437 separately records the signing host, maintains accepted identifiers per
environment, and rejects signatures outside that set. It does not belong to
this phase.

## Task 7: Render routable database addresses

Modify `tack/tack.env.j2:25-27,45,47-48` and
`ansible/inventory/group_vars/tack_all.yml:21-22` for TACK-352.

- [ ] Define `tack_guest_ipv6` per environment from
  `service_mapping.tack.ipv6` or `service_mapping.tack_qa_suburban.ipv6`.
  Replace SQL Compose hostnames with `[{{ tack_guest_ipv6 }}]`, including
  host-networked `tack_ops_database_url`.
- [ ] Deploy QA. Require `/healthz` 200 through the new SQL DSN, working ops
  database connections, and continued consumer writes.
- [ ] Test Kafka advertisement before changing `AUDIT_KAFKA_BROKERS=kafka:9092`
  at line 45. The broker advertises `kafka`, which resolves only inside Compose.
  Run from the QA guest network using the mapping address:

```bash
docker compose exec -T app sh -c 'true'   # Confirm the stack is running.
timeout 10 docker run --rm --network host edenhill/kcat:1.7.1 -b "[<qa guest ipv6>]:9092" -L
```

If metadata returns a guest-accessible advertised broker, replace line 45 with
the guest address and verify audit writes. Otherwise keep `kafka:9092` and
record that phase 5 must change broker addresses with its per-broker listeners.

- [ ] Lint and commit:

```bash
go run goodkind.io/configs/cmd/configs lint
git add tack/tack.env.j2 ansible/inventory/group_vars/tack_all.yml ansible/inventory/group_vars/tack_servers.yml ansible/inventory/group_vars/tack_qa_suburban_servers.yml
git commit -S -m "Render routable guest addresses in the tack environment

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 8: Schedule Docker pruning

For TACK-432, create the following service, timer, and installation task. Add
one host-preparation include in `ansible/playbooks/deploy-tack.yml`.

- [ ] Create `common/services/docker-prune.service`:

```ini
[Unit]
Description=Prune unused Docker data older than seven days
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
ExecStart=/usr/bin/docker system prune -af --filter until=168h
TimeoutStartSec=1800
```

Create `common/timers/docker-prune.timer`:

```ini
[Unit]
Description=Daily Docker prune

[Timer]
OnCalendar=*-*-* 09:00:00 UTC
RandomizedDelaySec=3600
Persistent=true

[Install]
WantedBy=timers.target
```

UTC fixes the schedule in early-morning Pacific time in the United States
regardless of guest timezone. Prune removes stopped containers, unused networks,
unreferenced images, and build cache. It does not remove volumes because the
command omits `--volumes`. Objects that the running stack references remain in use.

- [ ] Follow `tasks/package-updater.yml` in
  `ansible/playbooks/tasks/docker-prune.yml`. Copy the service to
  `/etc/systemd/system/docker-prune.service` and the timer to
  `/etc/systemd/system/docker-prune.timer`. Register both copy results and
  reload only when either changed:

```yaml
- name: Enable docker-prune timer
  ansible.builtin.systemd:
    name: docker-prune.timer
    enabled: true
    state: started
    daemon_reload: >-
      {{ docker_prune_service.changed or docker_prune_timer.changed }}
```

- [ ] Include `ansible.builtin.include_tasks: tasks/docker-prune.yml` after
  the Docker enable task, previously line 116.
- [ ] Deploy QA. Require a next-fire time from
  `systemctl list-timers docker-prune.timer`. Run
  `systemctl start docker-prune.service && journalctl -u docker-prune.service -n 20`
  and require completed pruning with reclaimed-space output and unchanged
  `docker ps` output. Commit:

```bash
git add common/services/docker-prune.service common/timers/docker-prune.timer ansible/playbooks/tasks/docker-prune.yml ansible/playbooks/deploy-tack.yml
git commit -S -m "Add a daily Docker prune timer to every tack guest

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 9: Approve and deploy production

- [ ] Ask the operator to approve the four new vault resources, conditional on
  capacity approval or completed reclamation, and the separated production
  deploy that changes addresses and recreates the app once. Do not change the
  signing key canceled in Task 6.
- [ ] Apply with explicit `-target` per new vault resource. Review
  `--check --diff` before `configs deploy deploy-tack --limit tack_servers`.
  Require preparation without stacks on new guests, guest 117 `/healthz` 200
  through new DSNs, continued consumer notarization, and armed prune timers on
  all five production guests.
- [ ] Record a verification comment on TACK-350, TACK-351, TACK-352, TACK-353,
  and TACK-432 before marking them Done. Phase 3 starts at TACK-354.

For every task, require configs lint for Ansible or OpenTofu validation and a
clean targeted plan, a QA dry-run render, and live verification of the changed
behavior. This phase verifies infrastructure through rendered playbooks and
live guests rather than unit tests.
