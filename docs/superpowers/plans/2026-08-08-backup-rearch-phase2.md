# Backup Rearchitecture Phase 2 (Provisioning Groundwork) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provision the multi-guest topology's groundwork in the configs repo: guest mapping, OpenTofu resources, per-guest identity, the deploy split, routable rendered addresses, the vault-stored audit signing key, and the per-guest Docker prune timer.

**Architecture:** Every change lands in the configs repo (`/Users/agoodkind/Sites/configs`). Four new production guests (three data, one app) and four testbed counterparts at id plus 100 are declared in `service_mapping.yml` and OpenTofu, with per-guest identity in `host_vars`. The deploy playbook splits into host preparation that runs on every guest and one-time steps that run on exactly one. The rendered environment drops loopback and compose-internal addresses in favor of mapping addresses. No service moves in this phase; phases 3 through 5 join the new guests to their clusters.

**Tech Stack:** OpenTofu (bpg/proxmox provider), Ansible via `go run goodkind.io/configs/cmd/configs`, systemd service and timer units, ansible-vault.

**Design of record:** `docs/superpowers/specs/2026-08-06-backup-rearch-design.md` (Provisioning section and phase 2 bullet).

**Tickets:** TACK-350 (guests), TACK-351 (deploy split), TACK-352 (routable addresses), TACK-353 (vault signing key), TACK-432 (prune timer).

## Global Constraints

- Testbed first, then prod. One environment per command. Validate every change on QA before any production apply or deploy.
- Guest ids and addresses exist only in `ansible/inventory/group_vars/all/service_mapping.yml`. Never hardcode a VMID or guest IPv6 anywhere else, including in commands you type; the testbed deploy that hit prod came from a hardcoded id.
- OpenTofu runs only through `go run goodkind.io/configs/cmd/configs tofu <command>`, which supplies the state backend and provider credentials from the vault. The testbed always names `-target=module.suburban`, because an untargeted run covers production. Production runs name `-target=module.vault`.
- Deploys run only through `go run goodkind.io/configs/cmd/configs deploy <playbook> [--limit <group>]`. Never invoke `ansible-playbook` directly. `--check --diff` before any mutating production run.
- Before any deploy: `git fetch origin` and fast-forward local main; deploys run against the origin/main commit.
- Production applies, production deploys, and destruction of the debianct sandbox guest each require explicit operator approval first. QA is autonomous.
- Secret values never appear in chat, logs, or command output. The signing key is generated and vaulted by the operator; agents handle only the variable name.
- `go run goodkind.io/configs/cmd/configs lint` must pass before every commit that touches Ansible; `tofu validate` before every commit that touches OpenTofu.
- Declare every input value in exactly one of three places: the service's group_vars, the service mapping, or OpenTofu. Never infer a value from whether it was set: no `default()`, no `is defined`, no `.get(key, default)`, no `| length` presence check, and no `lookup(..., default=)`. There is no per-line exemption, and the linter runs before every deploy.
- A per-group variable file must be named for its group exactly, `<group>_servers.yml`. A mismatched name loads nothing and fails silently.
- Give each guest a distinct Proxmox name across hypervisors. Two guests sharing a name merge into one inventory host, and the later plugin wins on conflicting attributes.
- These guests take a static address written from the mapping, so the pinned MAC is a stable link identity only. No address reservation is involved.
- Keep YAML lines under 90 columns; 120 is the hard limit.
- Commit subjects in imperative mood with the `Co-authored-by: Claude <noreply@anthropic.com>` trailer, signed (`git commit -S`).

## Fixed values this plan assigns

Copy these exactly; they were chosen against the current mapping so that no VMID collides on either hypervisor and every prod id plus 100 is free on suburban.

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

Sizing comes from the capacity reading, recorded in
`docs/superpowers/plans/2026-08-08-phase2-capacity.md`: testbed data guests
2 cores, 4096 MB, 40 GB disk; testbed app guest 2 cores, 2048 MB, 30 GB disk;
production data guests 4 cores, 16384 MB, 60 GB disk; production app guest
4 cores, 8192 MB, 40 GB disk. Container memory is a ceiling, not a
reservation, and the phase 2 guests carry no compose stack, so neither
hypervisor is stressed by this phase. Production memory needs two operator
decisions before phase 3 starts services on those guests: reclaim the
debianct sandbox guest, and shrink guest 117 as each data service migrates
off it.

The existing guest 117 (`tack`) becomes app instance one and data guest zero of nothing: it keeps serving the full stack until phases 3 through 5 move the data services onto the new guests.

---

### Task 1: Read real capacity on both hypervisors and record the sizing decision

**Files:**
- Create: `docs/superpowers/plans/2026-08-08-phase2-capacity.md` (in the tack repo, committed alongside this plan's execution record)

**Interfaces:**
- Produces: a committed capacity table and a go or reclaim verdict that Task 3 consumes before any `tofu apply`.

- [ ] **Step 1: Read vault's physical memory, free memory, and datastore headroom**

Run (read-only):

```bash
ssh root@vault.home.goodkind.io 'free -g; pvesm status; pct list'
```

If `vault.home.goodkind.io` does not resolve, take the hypervisor address from the `vault` entry in `ansible/inventory/hosts` in the configs repo; do not hardcode one here.

- [ ] **Step 2: Read suburban the same way**

Take the suburban hypervisor's address from its entry in `ansible/inventory/hosts` in the configs repo, then run the same read-only command against it:

```bash
ssh root@<suburban-address-from-inventory> 'free -g; pvesm status; pct list'
```

- [ ] **Step 3: Write the capacity table and verdict**

The file records: physical RAM per hypervisor, sum of configured guest memory before and after this plan, datastore free space against the new disk sum (prod: 360 GB on `local-lvm`; testbed: 150 GB on `local-zfs`), and one verdict line per hypervisor: `fits` or `requires debianct reclaim`. If the verdict is `requires debianct reclaim`, STOP and ask the operator; debianct (VMID 100, 8 cores, 16 GB) is approved for reclaim only with explicit confirmation before destruction.

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/plans/2026-08-08-phase2-capacity.md
git commit -S -m "Record hypervisor capacity reading for phase 2 sizing

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 2: Map the eight guests and the cluster groups in service_mapping

**Files:**
- Modify: `ansible/inventory/group_vars/all/service_mapping.yml` (services block near lines 62 to 85; `group_children` near line 214)

**Interfaces:**
- Consumes: the fixed-values table above.
- Produces: mapping keys `tack_data1` through `tack_app2_suburban` that every later task references via `service_mapping.<key>.ipv6` and `.vmid`; groups `tack_data_servers` and `tack_qa_data_suburban_servers`.

- [ ] **Step 1: Discover how inventory groups derive from guests**

```bash
cd /Users/agoodkind/Sites/configs
go run goodkind.io/configs/cmd/configs inventory-dump > /tmp/inv.json
```

Find how `tack_servers` and `tack_qa_suburban_servers` gain members (the Proxmox dynamic inventory in `ansible/inventory/vault.proxmox.yml` and `suburban.proxmox.yml` builds groups from guest metadata; read both files' `groups`/`keyed_groups` stanzas). Whatever attribute produces those two groups (tag set or hostname pattern), the new guests must carry the attribute values that produce `tack_data_servers` (prod data guests), `tack_app_servers` (prod app guests including 117 if the mechanism allows), `tack_qa_data_suburban_servers`, and `tack_qa_app_suburban_servers`. Record the mechanism in the commit message body.

- [ ] **Step 2: Add the eight service entries**

Follow the existing entry shape exactly (`hostname`, `vmid`, `ipv6`), inserting the four prod entries after `tack:` and the four suburban entries after `tack_qa_suburban:`, using the fixed-values table verbatim.

- [ ] **Step 3: Extend group_children**

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

- [ ] **Step 4: Lint and commit**

```bash
go run goodkind.io/configs/cmd/configs lint
git add ansible/inventory/group_vars/all/service_mapping.yml
git commit -S -m "Map the phase 2 tack data and app guests with cluster groups

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 3: Declare the eight OpenTofu container resources

**Files:**
- Create: `opentofu/vault/tack_data.tf` (three data resources plus `tack_app2`)
- Create: `opentofu/suburban/tack_data_qa.tf` (four testbed resources)

**Interfaces:**
- Consumes: Task 1's verdict (`fits` required, or reclaim completed), Task 2's mapping keys.
- Produces: running, SSH-reachable Debian 13 LXCs at the eight addresses.

- [ ] **Step 1: Write the vault resources**

Copy the shape of `opentofu/vault/containers.tf:1-66` (the `tack` resource) per guest, changing: resource name (`tack_data1` etc.), `vm_id = local.service_mapping.tack_data1.vmid`, hostname and address and MAC from the fixed table, `memory`/`cpu`/`disk` from the sizing table, tags `["lxc", "tack", "tack-data", "docker"]` for data guests and `["lxc", "tack", "tack-app", "docker"]` for `tack_app2`, adjusted to whatever grouping attribute Task 2 Step 1 discovered. Keep `prevent_destroy = true` and the same `ignore_changes` list. Take the address and gateway from `local.service_mapping` lookups, not literals (the existing `tack` resource hardcodes them; do not copy that).

- [ ] **Step 2: Write the suburban resources**

Copy the shape of `opentofu/suburban/tack_qa.tf` (mapping-driven addresses, `local-zfs`, trunk bridge reference, no `start_on_boot`) per testbed guest with the fixed table's values and the testbed sizing.

- [ ] **Step 3: Validate and plan**

`opentofu/` is a single root module whose children are `module.suburban` and `module.vault`; the per-environment directories carry no backend or provider configuration, so a command run from inside one of them fails on a missing provider. State lives in a Cloudflare R2 bucket, and its credentials come from the vault through the repo control tool, so every command runs through that tool rather than calling `tofu` directly:

```bash
cd /Users/agoodkind/Sites/configs
go run goodkind.io/configs/cmd/configs tofu init
go run goodkind.io/configs/cmd/configs tofu plan -target=module.suburban
```

Expected: exactly `4 to add, 0 to change, 0 to destroy`. Any nonzero change or destroy count is a stop condition; report the resource named instead of proceeding. Never run an untargeted plan or apply, and never fall back to one when a targeted command fails: an untargeted run covers production.

The production plan is the same command with `-target=module.vault`, run only at Task 9.

- [ ] **Step 4: Commit, then apply testbed only**

```bash
git add opentofu/vault/tack_data.tf opentofu/suburban/tack_data_qa.tf
git commit -S -m "Declare the phase 2 tack data and app guest containers

Co-authored-by: Claude <noreply@anthropic.com>"
```

Apply suburban (QA is autonomous), then verify each new testbed guest answers SSH at its mapping address. The vault apply waits for operator approval and Task 1's verdict.

---

### Task 4: Per-guest identity in the mapping and per-guest group files

The repository's declaration rule names exactly three homes for an input value: the service's group_vars, the service mapping, or OpenTofu. `host_vars` is not one of them; the single existing `host_vars` file exists only to override a value the inventory plugin composes wrongly, which is a precedence fix rather than a declaration. This task therefore uses the mapping for guest identity and a per-guest group file for guest configuration.

The mapping already creates one `<key>_servers` group per entry, so every guest has a group of its own and per-guest values have a documented home.

**Files:**
- Modify: `ansible/inventory/group_vars/all/service_mapping.yml` (add the Docker /96 and its gateway to each tack guest entry, beside the address and MAC that already live there)
- Create: ten files under `ansible/inventory/group_vars/`, one per guest, named `<mapping-key>_servers.yml`, carrying that guest's role and provisioning flag
- Modify: the two environment group files, removing the Docker subnet values that move to the mapping

**Interfaces:**
- Produces: `docker_v6_subnet` and `docker_v6_gateway` on each tack guest's mapping entry; per-guest `tack_cluster_role` (`data` on the three data guests, `app` on the app guests) and `tack_provision_owner` (`true` on exactly one guest per environment, `false` elsewhere). Task 5 consumes `tack_provision_owner`; phase 3 consumes `tack_cluster_role`.

- [ ] **Step 1: Move the Docker subnets into the mapping**

Each tack guest entry gains `docker_v6_subnet` and `docker_v6_gateway`. Guests 117 and 217 carry the values they use today, read from the two environment group files before those lines are removed. Each new guest takes a distinct /96 from the fixed table, with its gateway as that prefix plus `::1`. The environment group files then read the mapping, so the /96 lives in one place per guest and a renumber changes one line.

- [ ] **Step 2: Write the ten per-guest group files**

Each file is named for the guest's own group, `<mapping-key>_servers.yml`, and carries only that guest's `tack_cluster_role` and `tack_provision_owner`. A file name that does not match the group exactly loads nothing, silently.

- [ ] **Step 3: Render-check on QA, lint, commit**

```bash
go run goodkind.io/configs/cmd/configs deploy deploy-tack --limit tack_qa_suburban_servers --check --diff
go run goodkind.io/configs/cmd/configs lint
git add ansible/inventory
git commit -S -m "Declare tack guest Docker subnets in the mapping and per-guest roles in group vars

Co-authored-by: Claude <noreply@anthropic.com>"
```

Expected from `--check --diff`: zero changes on guest 217, because the values moved rather than changed.

---

### Task 5: Split deploy-tack into every-guest prep and single-owner provisioning (TACK-351)

**Files:**
- Modify: `ansible/playbooks/deploy-tack.yml`

**Interfaces:**
- Consumes: `tack_provision_owner` from Task 4.
- Produces: a play layout later phases extend without touching the gating.

- [ ] **Step 1: Retarget the host-prep play at every tack guest**

The import at `deploy-tack.yml:15-18` and the main play's `hosts:` at line 20-21 currently name `tack_servers:tack_qa_suburban_servers`. Both become `tack_all` (the Task 2 parent group), so prep (apt, Docker, sysctl, ndppd, daemon.json) reaches data and app guests alike.

- [ ] **Step 2: Gate the one-time steps on the provisioning owner**

The tasks at lines 125 (create `/etc/tack`), 133 (signing key, rewritten in Task 6), 152 (create `/etc/foundationdb`), and 269 (`ops provision`: migrations, gated fdb configure, audit roles, seed) move into a second play with `hosts: tack_all` and a play-level `when: tack_provision_owner`. Compose fetch and `docker compose up -d` also stay owner-only in this phase: the new guests get no stack yet; phases 3 through 5 bring their services.

- [ ] **Step 3: Verify both environments render, commit**

```bash
go run goodkind.io/configs/cmd/configs deploy deploy-tack --limit tack_qa_suburban_servers --check --diff
go run goodkind.io/configs/cmd/configs lint
git add ansible/playbooks/deploy-tack.yml
git commit -S -m "Split deploy-tack into all-guest prep and provision-owner plays

Co-authored-by: Claude <noreply@anthropic.com>"
```

Then run the real QA deploy limited to the QA data group and confirm: Docker present on all three new testbed data guests, no compose stack started on them, and guest 217's stack untouched (`docker ps` unchanged, healthz still 200).

---

### Task 6: Audit signing key from vault (TACK-353)

**Files:**
- Modify: `ansible/playbooks/deploy-tack.yml` (the generate task from the old line 133)
- Modify: `ansible/inventory/group_vars/tack_servers.yml`, `tack_qa_suburban_servers.yml` (reference the new vault names)

**Interfaces:**
- Consumes: vault keys `vault_tack_audit_signing_key` and `vault_tack_qa_audit_signing_key` (PEM contents), which the OPERATOR adds; agents never see the values.
- Produces: `/etc/tack/audit-signing.pem` rendered from vault, mode 0600, identical on every guest of an environment.

- [ ] **Step 1: Operator adds the two vault secrets**

Hand the operator these commands to run locally (never in an agent shell); each generates a key and pastes it into the vault editor session:

```bash
openssl genpkey -algorithm ed25519 -out /tmp/tack-audit-signing.pem
# paste contents as vault_tack_audit_signing_key via the repo's vault edit flow, then
shred -u /tmp/tack-audit-signing.pem
# repeat for vault_tack_qa_audit_signing_key
```

STOP until `go run goodkind.io/configs/cmd/configs keys` lists both names.

- [ ] **Step 2: Replace generation with a vault-sourced copy**

The `openssl genpkey` task (old line 133-139) becomes:

```yaml
    - name: Install the audit signing key from vault
      ansible.builtin.copy:
        content: "{{ tack_audit_signing_key }}"
        dest: "{{ tack_audit_signing_key_path }}"
        owner: root
        group: root
        mode: "0600"
      no_log: true
```

with `tack_audit_signing_key: "{{ vault_tack_audit_signing_key }}"` declared in `tack_servers.yml` and the qa variant in `tack_qa_suburban_servers.yml` (this is a real per-environment override, permitted by the vault contract). `no_log: true` is mandatory.

- [ ] **Step 3: Deploy QA, verify signer continuity, commit**

Deploy QA, then confirm the consumer still notarizes (a new `audit.notarizer.signed` line within two minutes of restart) and no signature verification errors appear. The schema records the signing key id per signature, so the key change is a rotation, not a break. Commit:

```bash
git add ansible/playbooks/deploy-tack.yml ansible/inventory/group_vars/tack_servers.yml ansible/inventory/group_vars/tack_qa_suburban_servers.yml
git commit -S -m "Render the audit signing key from vault instead of per-host generation

Co-authored-by: Claude <noreply@anthropic.com>"
```

Production keeps its current per-host key until the operator-approved prod deploy renders the vault key.

---

### Task 7: Routable addresses in the rendered environment (TACK-352)

**Files:**
- Modify: `tack/tack.env.j2:25-27,45,47-48`
- Modify: `ansible/inventory/group_vars/tack_all.yml:21-22` (`tack_ops_database_url`)

**Interfaces:**
- Consumes: `service_mapping.tack.ipv6` / `service_mapping.tack_qa_suburban.ipv6` via the existing per-group variable pattern.
- Produces: a rendered `.env` with no loopback and no compose-internal DNS names except the Kafka broker list (see Step 3).

- [ ] **Step 1: Point the SQL and ClickHouse strings at the guest address**

Lines 25-27 (`@yugabyte:5433`) and 47-48 (`@clickhouse:9000`) replace the compose DNS name with `[{{ tack_guest_ipv6 }}]`, where `tack_guest_ipv6` is a new per-group variable set to the mapping address in `tack_servers.yml` and `tack_qa_suburban_servers.yml`. Both ports are published on the guest, so the address works from bridge and host-network containers alike. `tack_ops_database_url` in `tack_all.yml` swaps `[::1]` for the same variable (move the declaration into the two per-group files, since `tack_all.yml` cannot reference a per-environment address).

- [ ] **Step 2: Verify on QA**

Deploy QA; then confirm the app serves `/healthz` 200 (it probes the SQL pool through the new DSN), `ops` commands still reach the database, and the audit consumer still writes.

- [ ] **Step 3: Kafka flip is conditional, decided by one measurement**

`AUDIT_KAFKA_BROKERS=kafka:9092` (line 45) may not flip blind: Kafka clients bootstrap to the given address but then follow the broker's advertised listener, which is `kafka` today and resolves only inside the compose network. On QA, run from the guest (host network):

```bash
docker compose exec -T app sh -c 'true'   # confirm stack up first
timeout 10 docker run --rm --network host edenhill/kcat:1.7.1 -b "[<qa guest ipv6>]:9092" -L
```

(Read the QA guest address from service_mapping; do not hardcode it.) If the metadata listing succeeds and names a reachable broker address, flip line 45 to the guest address and re-verify audit events flow. If it fails on the advertised listener, leave line 45 as `kafka:9092` and record in the commit body that the broker flip rides with phase 5's per-broker advertised addresses; the spec's phase 5 owns that change.

- [ ] **Step 4: Lint and commit**

```bash
go run goodkind.io/configs/cmd/configs lint
git add tack/tack.env.j2 ansible/inventory/group_vars/tack_all.yml ansible/inventory/group_vars/tack_servers.yml ansible/inventory/group_vars/tack_qa_suburban_servers.yml
git commit -S -m "Render routable guest addresses in the tack environment

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 8: Docker prune timer as its own handler (TACK-432)

**Files:**
- Create: `common/services/docker-prune.service`
- Create: `common/timers/docker-prune.timer`
- Create: `ansible/playbooks/tasks/docker-prune.yml`
- Modify: `ansible/playbooks/deploy-tack.yml` (one `include_tasks` line in the host-prep play)

**Interfaces:**
- Consumes: nothing from other tasks; independent.
- Produces: a daily prune on every tack guest.

- [ ] **Step 1: Write the units**

`common/services/docker-prune.service`:

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

`common/timers/docker-prune.timer`:

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

The `UTC` suffix pins the schedule regardless of each guest's configured timezone, placing the run in early-morning US Pacific. `docker system prune -a` removes stopped containers, unused networks, unreferenced images, and build cache. It never touches volumes, because `--volumes` is absent, so no persisted data is at risk. Anything the running compose stack references counts as in use and survives.

- [ ] **Step 2: Write the task file following the package-updater idiom**

`ansible/playbooks/tasks/docker-prune.yml` mirrors `tasks/package-updater.yml`: two `ansible.builtin.copy` tasks (service to `/etc/systemd/system/docker-prune.service`, timer to `/etc/systemd/system/docker-prune.timer`), each registering a result, then:

```yaml
- name: Enable docker-prune timer
  ansible.builtin.systemd:
    name: docker-prune.timer
    enabled: true
    state: started
    daemon_reload: >-
      {{ docker_prune_service.changed or docker_prune_timer.changed }}
```

- [ ] **Step 3: Include it from the host-prep play**

One line in `deploy-tack.yml`'s prep play, after the Docker enable task (old line 116): `ansible.builtin.include_tasks: tasks/docker-prune.yml`.

- [ ] **Step 4: Deploy QA, verify, commit**

Deploy QA, then on the QA guest: `systemctl list-timers docker-prune.timer` shows a next-fire time, and `systemctl start docker-prune.service && journalctl -u docker-prune.service -n 20` shows a completed prune with reclaimed-space output while `docker ps` afterward is unchanged. Commit:

```bash
git add common/services/docker-prune.service common/timers/docker-prune.timer ansible/playbooks/tasks/docker-prune.yml ansible/playbooks/deploy-tack.yml
git commit -S -m "Add a daily Docker prune timer to every tack guest

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 9: Production rollout (operator-gated)

**Files:** none new; applies Tasks 3 through 8 to production.

- [ ] **Step 1: Operator approvals**

Ask, in one question: (a) vault `tofu apply` targeting the four new resources (contingent on Task 1's `fits` verdict or completed reclaim), (b) production deploy of the split playbook, which also swaps the signing key and the rendered addresses and restarts the app containers once.

- [ ] **Step 2: Apply and deploy**

`tofu apply` with explicit `-target` per new vault resource; then `configs deploy deploy-tack --limit tack_servers` preceded by `--check --diff`. Verify afterward exactly as on QA: prep landed on the new guests with no stack started, guest 117 serves `/healthz` 200 through the new DSNs, the consumer notarizes under the vault key, and the prune timer is armed on all five prod guests.

- [ ] **Step 3: Close the tickets**

TACK-350, TACK-351, TACK-352, TACK-353, TACK-432 move to Done with a verification comment each; phase 3 (TACK-354 onward) unblocks.

## Verification summary

Every task gates on `configs lint` (Ansible) or `tofu validate` plus a clean targeted plan (OpenTofu), a QA `--check --diff` render before mutation, and a live QA verification of the specific behavior it changes. There are no unit tests in this phase; the playbook render and the live guest are the test surface, matching how this repo verifies infrastructure changes.
