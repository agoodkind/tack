# Tack QA build plan

This plan describes how to stand up `tack-qa`, a long-lived QA environment for
Tack on the suburban Proxmox hypervisor, mirroring the production CT 117
deployment closely enough that wave migrations, seed runs, backfills, and
restore drills can all be exercised against real-shape data before production
is touched. It is the input document for three tickets:

- TACK-243: provision the `tack-qa` LXC container on suburban via OpenTofu.
- TACK-244: adapt the Ansible playbook to deploy the Tack Compose stack with
  QA env vars.
- TACK-245: build a data-gen script that exercises every Tack code path on
  `tack-qa`.

The plan is the source of opinionated decisions; the tickets carry only the
short version. Where a decision could not be made confidently from inspection,
the plan calls it out in section 9 instead of guessing.

All inspection that produced this plan was read-only, against tack
(`3d06:bad:b01::117`), suburban (`10.240.0.148`), vault (`3d06:bad:b01::254`),
and the local repos at `/Users/agoodkind/Sites/configs` and
`/Users/agoodkind/Sites/tack`.

---

## Section 1: production state inventory

This section is what `tack-qa` needs to mirror. All data comes from live
read-only SSH to `tack` on 2026-05-09.

### 1.1 Compose stack at `/root/tack/docker-compose.yml`

The production Compose stack defines six services and a custom IPv6-only
bridge network. Image tags, mounts, and networking are reproduced verbatim
because QA must use the same versions to be a useful pre-production gate.

Services in order:

- `app`
  - Image: `tack-server:latest`, built from the repo's top-level Dockerfile
    on the host (no registry pull). Build is driven by `make deploy`'s
    `docker build --network host` step.
  - Ports: `${PORT:-8000}:8000`.
  - DNS pinned to `127.0.0.11` so embedded Docker DNS handles AAAA-only
    inter-container resolution.
  - Environment: `DATABASE_URL`, `AUDIT_WRITER_DSN`, `AUDIT_READER_DSN`,
    `AUDIT_REDACTOR_DSN`, `FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster`,
    `MEILI_URL=http://meilisearch:7700`, `MEILI_MASTER_KEY`,
    `TEMPORAL_ADDRESS=temporal:7233`, `PORT=8000`, `ENV=development`,
    `LOG_LEVEL`, `LOG_MAX_BACKUPS`, `LOG_MAX_AGE_DAYS`, `LOG_MAX_SIZE_MB`,
    `LOG_JSON_FILE`, `LOG_TEXT_FILE`, `AUDIT_SIGNING_KEY_PATH`,
    `AUDIT_WAL_DIR`.
  - Volumes: `/etc/foundationdb:/etc/foundationdb:ro`,
    `tack-logs:/var/log/tack`, `/etc/tack:/etc/tack:ro` (carries the audit
    signing key generated once on the host with
    `openssl genpkey -algorithm ed25519 -out /etc/tack/audit-signing.pem`),
    `tack-audit-wal:/var/lib/tack/audit-wal`.
  - Depends on: `yugabyte`, `fdb`, `meilisearch`, `temporal` (all
    `service_healthy`).

- `yugabyte`
  - Image: `yugabytedb/yugabyte:2024.2.8.0-b85`. The 2024.2.x line is the
    first that supports IPv6 in `yugabyted` (issue #24665).
  - Command: a bash wrapper that asserts an IPv6 NSS entry exists for the
    service name `yugabyte`, then runs `yugabyted start --daemon=false
    --base_dir=/home/yugabyte/var --advertise_address=yugabyte
    --listen=yugabyte`. The `--base_dir` flag is critical: without it
    `yugabyted` defaults to `~/var` on the container overlay and a
    `docker compose down` wipes the SQL side (the 2026-04-28 incident).
  - Environment: `YSQL_DB`, `YSQL_USER`, `YSQL_PASSWORD`.
  - Ports: `5433:5433` (YSQL), `7000:7000` (master UI), `9000:9000`
    (tserver UI).
  - Volume: `yugabyte-data:/home/yugabyte/var`.
  - Healthcheck: `ysqlsh -h yugabyte -p 5433 -U $YUGABYTE_USER -d $YUGABYTE_DB
    -c 'SELECT 1'`.

- `fdb`
  - Image: `foundationdb/foundationdb:${FDB_VERSION:-7.4.6}`. The version
    is pinned in three places (Makefile, Dockerfile build args, Go
    bindings); QA must use the same version.
  - Environment: `FDB_NETWORKING_MODE=container`, `FDB_PORT=4500`,
    `FDB_PROCESS_CLASS=unset`.
  - Volumes: `fdb-data:/var/fdb`,
    `/etc/foundationdb:/etc/foundationdb`,
    `/root/tack/fdb-overlay/fdb.bash:/var/fdb/scripts/fdb.bash:ro` (the
    patched fdb startup script with IPv6 bracket handling).
  - Healthcheck: `fdbcli --exec 'status minimal'` matches `available`.

- `meilisearch`
  - Image: `getmeili/meilisearch:v1.12`.
  - Environment: `MEILI_MASTER_KEY`, `MEILI_ENV=production`,
    `MEILI_HTTP_ADDR=[::]:7700`. The explicit v6 listen address is
    required: without it Meilisearch binds 0.0.0.0:7700 (v4 only) and the
    app cannot reach it on the AAAA-only bridge.
  - Ports: `7700:7700`.
  - Volume: `meili-data:/meili_data`.

- `temporal-db`
  - Image: `postgres:16-alpine`.
  - Environment: hardcoded `temporal/temporal/temporal` (Temporal-internal
    only; not a Tack secret).
  - Volume: `temporal-db-data:/var/lib/postgresql/data`.

- `temporal`
  - Image: `temporalio/auto-setup:1.26.2`.
  - Environment: `DB=postgres12`, `DB_PORT=5432`, `POSTGRES_USER=temporal`,
    `POSTGRES_PWD=temporal`, `POSTGRES_SEEDS=temporal-db`.
  - Ports: `7233:7233`.

- `temporal-ui`
  - Image: `temporalio/ui:2.34.0`.
  - Environment: `TEMPORAL_ADDRESS=temporal:7233`.
  - Ports: `8080:8080`.

Volumes declared at the top level: `yugabyte-data`, `fdb-data`, `meili-data`,
`temporal-db-data`, `tack-logs`, `tack-audit-wal`.

Network declared at the top level (`networks.default`):

- `enable_ipv6: true`, `enable_ipv4: false` (forced v6-only).
- `driver_opts: com.docker.network.bridge.gateway_mode_v6: routed` (no v6
  masquerade; containers source out as real GUAs).
- IPAM subnet `3d06:bad:b01:0:7ac::/96`, gateway
  `3d06:bad:b01:0:7ac::1`.

### 1.2 .env variable names (production)

The names below come from `grep -E '^[A-Z_]+=' /root/tack/.env` on tack.
Values are intentionally not captured.

- `YUGABYTE_USER`
- `YUGABYTE_PASSWORD`
- `YUGABYTE_DB`
- `MEILI_MASTER_KEY` (referenced in the compose file but not present in the
  current `.env`; falls back to the dev default. QA should set its own.)
- `AUDIT_WRITER_DSN`
- `AUDIT_READER_DSN`
- `AUDIT_REDACTOR_DSN`
- `AUDIT_WRITER_PASSWORD`
- `AUDIT_READER_PASSWORD`
- `AUDIT_REDACTOR_PASSWORD`
- `AUDIT_SIGNING_KEY_PATH` (path, not secret; defaults to
  `/etc/tack/audit-signing.pem`).

Environment variables referenced by the Compose file but not in `.env`
(they fall through to compose defaults): `PORT`, `LOG_LEVEL`,
`LOG_MAX_BACKUPS`, `LOG_MAX_AGE_DAYS`, `LOG_MAX_SIZE_MB`, `LOG_JSON_FILE`,
`LOG_TEXT_FILE`, `AUDIT_WAL_DIR`, `FDB_VERSION`.

### 1.3 Host-level setup outside Compose

Three settings on the host are required for the Compose stack to function.
QA must replicate all three.

- `/etc/sysctl.d/99-tack-ipv6.conf` (live):
  ```
  net.ipv6.conf.all.forwarding = 1
  net.ipv6.conf.eth0.proxy_ndp = 1
  ```

- `/etc/ndppd.conf` (live):
  ```
  route-ttl 30000
  proxy eth0 {
     router yes
     timeout 500
     ttl 30000
     rule 3d06:bad:b01:0:7ac::/96 {
        auto
     }
  }
  ```
  The `ndppd` daemon must be installed and running. The proxied prefix is
  the Compose IPAM subnet.

- `/etc/docker/daemon.json` (live):
  ```
  {
    "ipv6": true,
    "ip6tables": true
  }
  ```

In addition, the following host-side artifacts must exist before the app
starts:

- `/etc/tack/audit-signing.pem` (Ed25519 key). Create once with
  `openssl genpkey -algorithm ed25519 -out /etc/tack/audit-signing.pem`.
- `/etc/foundationdb/` (directory). Created on first FDB run; the `fdb`
  service mounts it read-write to write its cluster file.
- `/root/tack/` (the rsync target for `make deploy`). Includes
  `docker-compose.yml`, `.env`, `fdb-overlay/fdb.bash`, and `scripts/`.

### 1.4 Container network shape

CT 117 sits on the production `3d06:bad:b01::/64` segment as `::117`. The
NDP-proxied `/96` is carved from the host's on-link `/64`, so no static
route on the upstream router is needed. Routes seen on tack:

```
3d06:bad:b01:0:7ac::/96 dev br-c8e93789960e proto kernel metric 256
3d06:bad:b01::/64       dev eth0           proto kernel metric 256
default via 3d06:bad:b01::1 dev eth0 proto kernel metric 1024 onlink
```

QA on suburban will not be on the same `/64`, which is the central design
choice driving section 3.

---

## Section 2: suburban current state inventory

All data comes from `pct list`, `qm list`, `pveversion`, and
`/etc/network/interfaces` on suburban on 2026-05-09.

### 2.1 Existing guests

LXC containers:

- 100 `mwan-failover-test` (running, MWAN testbed)
- 200 `isp-webpass` (running, MWAN testbed)
- 201 `isp-att` (running, MWAN testbed)
- 202 `isp-mbrains` (running, MWAN testbed)
- 203 `testbed-proxy` (running, MWAN testbed LAN client)

QEMU VMs:

- 101 `opnsense-test` (2 GB)
- 102 `opnsense-test2` (4 GB)
- 129 `opnsense-serial-practice` (4 GB)
- 130 `opnsense-serial-rehearsal` (4 GB)
- 950 `test-mwan` (2 GB)

`pvesh get /cluster/nextid` returns `103`. The lowest free LXC ID in the
mwan-testbed range is therefore `103`. Other free IDs verified by direct
`pct config <id>` probe: 117, 118, 119, 120, 121, 204, 205, 206, 250.

### 2.2 Bridges

From `/etc/network/interfaces` on suburban:

- `vmbr0`: `10.240.0.148/24`, IPv6 SLAAC. Bridge port `nic0`. This is the
  Comcast-facing LAN bridge with public reachability.
- `vmbr1`: `10.240.200.1/24`, IPv6 `3d06:bad:b01:200::1/64`,
  `fe80::1/64`. No bridge ports. This is the VM segment, reachable only
  via WireGuard from vault (`3d06:bad:b01::/56` is allowed).
- `vmbr2`: `10.250.250.5/29`, `3d06:bad:b01:201::5/64`. MWAN testbed
  mwanbr.
- `vmbr3`: `192.168.1.5/24`, `3d06:bad:b01:211::5/64`. OPNsense LAN
  bridge.
- `vmbr4`, `vmbr5`, `vmbr6`: empty bridges for ISP simulators.
- `vmbrtrunk`: VLAN-aware trunk for OPNsense iavf0 parity.

### 2.3 Available IPv6 prefixes

`3d06:bad:b01:200::/64` is the natural home for QA: it is the suburban VM
segment, it is reachable from vault and the developer's laptop via the
healthy WireGuard tunnel (the WG peer config allows `3d06:bad:b01::/56`),
and it has no inbound public reachability without explicit Cloudflare
tunnel routing.

Within `3d06:bad:b01:200::/64`, the testbed already uses `::100` (LXC 100
through eth0 inside that container) and `::950` (VM 950). A clean range
for new QA-class guests is `3d06:bad:b01:200:1::/80`. The first concrete
address proposed for `tack-qa` is `3d06:bad:b01:200:1::117` (suffix `:117`
chosen to mirror the production CT 117 mnemonic).

The NDP-proxied `/96` shape that production uses (carving a sub-prefix
from the host's on-link `/64` for Docker containers) maps cleanly onto
suburban: from `3d06:bad:b01:200::/64`, a `/96` carve such as
`3d06:bad:b01:200:1:7ac::/96` keeps the production-style key constant
`7ac` and stays clear of any existing testbed allocations. The actual
`/96` value can be picked at apply time; the plan only commits to the
shape.

### 2.4 Host shape

- Proxmox VE 9.1.6, kernel 6.17.13-2-pve.
- Debian 13 base.
- Docker is not installed on the host, which is fine: Docker runs inside
  the LXC, not on the hypervisor.
- WireGuard tunnel `wg0` is up: peer
  `jz3eKGui8bC2vf9rxrKGbk6WGwQWIc/PRNpFW91xDkk=` (vault), latest
  handshake 1m46s ago at probe time, allowed IPs include
  `3d06:bad:b01::/56`. This is the access path for the operator and for
  any backup transfer from CT 117.
- Storage: `local` (dir, 1.2 TB free), `local-zfs` (zfspool, 1.2 TB
  free). `local-lvm` is disabled. `local-zfs` is the right datastore for
  the QA LXC because the existing testbed LXCs (100, 200-203) all use it
  and the snapshot story is cleaner there.
- LXC templates available on `local`:
  `debian-13-standard_13.1-2_amd64.tar.zst`. This is the same template
  the production tack LXC uses.

---

## Section 3: Tofu module design (TACK-243 input)

### 3.1 LXC ID, hostname, address

- LXC ID: `103`. Lowest free ID returned by `pvesh get /cluster/nextid`
  on suburban, and unused by any other testbed container.
- Hostname: `tack-qa`. Bare hostname. Whether it gets an
  `*.qa.goodkind.io` FQDN is open in section 9.
- IPv6 address on `vmbr1`: `3d06:bad:b01:200:1::117/64`. Gateway is
  `3d06:bad:b01:200::1` (the suburban host's stub address on `vmbr1`).
- IPv4: not assigned. Mirrors production's IPv6-only posture, and the
  Compose bridge inside the LXC is forced v6-only anyway.

### 3.2 Volume layout

- Root volume: `local-zfs:subvol-103-disk-0`, size `40 GB`, matching
  production's `local-lvm:vm-117-disk-0` size. Headroom for one
  Yugabyte snapshot plus one in-flight backup directory.
- No separate data volume. Backups land in `/root/backups/tack-<TS>/`
  inside the LXC just like production. If QA backups grow large enough
  to need their own dataset, the plan is to add a second mount in a
  follow-up; for the initial build, 40 GB on root is sufficient because
  QA holds a synthetic dataset, not a year of audit history.

### 3.3 Resource limits

- CPU: `2` cores (matches production).
- Memory: `8192` MB (matches production CT 117). Yugabyte alone uses
  ~3 GB on idle; FDB plus Meilisearch plus the app fit comfortably in
  8 GB.
- Swap: `512` MB (matches the testbed shape).
- Features: `nesting=1`. Required for Docker overlay inside LXC.
- Privilege: `unprivileged = true` (matches production CT 117).

### 3.4 Module input variables

The module is a parameterized version of the production
`proxmox_virtual_environment_container.tack` resource at
`/Users/agoodkind/Sites/configs/opentofu/containers.tf:402`, with the
suburban provider alias and QA-specific values. To make the module
reusable for future QA-like environments without copy-paste, propose
these inputs:

- `vm_id` (number, required): the Proxmox VMID.
- `node_name` (string, default `"hypervisor"`): the Proxmox node.
- `provider_alias` (string, default `"proxmox.suburban"`): selects
  which provider block in `providers.tf` to use.
- `hostname` (string, required): bare hostname.
- `ipv6_address` (string, required): including the prefix length, e.g.
  `3d06:bad:b01:200:1::117/64`.
- `ipv6_gateway` (string, required): `3d06:bad:b01:200::1` for QA on
  suburban.
- `bridge` (string, default `"vmbr1"`).
- `mac_address` (string, required): a stable BC:24:11:... value.
- `disk_size_gb` (number, default `40`).
- `memory_mb` (number, default `8192`).
- `cpu_cores` (number, default `2`).
- `tags` (list(string), default `["lxc", "tack", "qa", "docker"]`).
- `template_file_id` (string, default
  `"local:vztmpl/debian-13-standard_13.1-2_amd64.tar.zst"`).
- `datastore_id` (string, default `"local-zfs"`).
- `prevent_destroy` (bool, default `true`). Lets the operator flip to
  `false` only during a deliberate teardown.
- `github_ssh_keys_user` (string, default `"agoodkind"`): consumed via
  the existing `data.http.github_ssh_keys` data source.

Where this module lives: a new file at
`/Users/agoodkind/Sites/configs/opentofu/tack_qa.tf` is the simplest
shape. Pulling out a real Tofu module (under
`opentofu/modules/tack_lxc/`) is a nice-to-have but not required for
TACK-243; the existing repo style is one resource per file. Recommend
the single-file shape for the first cut and refactor later if a third
QA-like environment shows up.

### 3.5 Provider configuration

Both providers already exist in
`/Users/agoodkind/Sites/configs/opentofu/providers.tf`:

- `provider "proxmox"` (default, vault).
- `provider "proxmox"` aliased to `suburban`.

The QA resource uses `provider = proxmox.suburban`. No new provider
block is needed.

### 3.6 Tofu apply flow

- State backend: Consul KV at `[3d06:bad:b01::106]:8500`, path
  `opentofu/state` (already configured in `backend.tf`). QA uses the
  same state file as everything else, which is correct: the Tofu state
  is the single source of truth for all infra.
- Secrets handling: `var.suburban_proxmox_api_token` already exists in
  `terraform.tfvars` (gitignored). No new secret needed for TACK-243.
- Drift detection: `tofu plan` after apply should report no changes.
  The pattern from `containers.tf` of `lifecycle.ignore_changes =
  [operating_system[0].template_file_id]` should be copied into the
  QA resource because Proxmox does not store the template name on
  imported configs.
- Apply command: `cd /Users/agoodkind/Sites/configs/opentofu &&
  tofu plan -target=proxmox_virtual_environment_container.tack_qa`
  then `tofu apply` of the same target. The `prevent_destroy` flag
  protects accidental teardown.

---

## Section 4: Ansible playbook adaptation (TACK-244 input)

### 4.1 Existing playbook state

The playbook at
`/Users/agoodkind/Sites/configs/ansible/playbooks/deploy-tack.yml`
exists. It is a thin Compose-stack deployer that:

1. Imports `prep-guests.yml` for SSH keys and base config.
2. Installs Docker CE.
3. Renders `tack/tack.env.j2` to `/opt/tack/.env`.
4. Copies `tack/docker-compose.yml` to `/opt/tack/docker-compose.yml`.
5. Runs `docker compose up -d`.

Important discovery: the files at
`/Users/agoodkind/Sites/configs/tack/docker-compose.yml` and
`/Users/agoodkind/Sites/configs/tack/tack.env.j2` are stale. They
describe a single-DB postgres-only stack
(`image: ghcr.io/agoodkind/tack:latest`, `service: db (postgres:16)`,
single `DATABASE_URL`). Production is the six-service stack documented
in section 1.1.

The production Compose file is shipped to CT 117 by `make deploy`'s
rsync, not by this Ansible playbook. The playbook has effectively never
deployed the real Tack stack. It is fine for prep but the Compose
contents need to be replaced before TACK-244 can deploy QA.

### 4.2 Variables that must be lifted to inventory

These are values that are hardcoded in the production setup and would
clash if QA reused them:

- `service_hostname`: production is `tack.home.goodkind.io` from
  `service_mapping.tack.hostname`. QA should be `tack-qa` (or
  `tack-qa.home.goodkind.io` if the operator wants the FQDN; see
  section 9).
- `service_ipv6_address`: production is `3d06:bad:b01::117/64`. QA is
  `3d06:bad:b01:200:1::117/64`.
- `service_mac_address`: production is `bc:24:11:a3:52:17`. QA needs a
  fresh stable MAC.
- `tack_install_dir`: production uses `/opt/tack` per the playbook
  variable, but the actual production deploy uses `/root/tack`
  (because `make deploy` rsyncs to `tack:/root/tack/`). QA should be
  `/root/tack` to keep one shape for both environments, and the
  playbook variable should be updated to match the live production
  truth.
- `tack_image`: irrelevant for the real stack (image is built in
  place from the rsync'd source as `tack-server:latest`).
- `tack_db_password`, `audit_*_password`, `meili_master_key`,
  `audit_signing_key`: each is generated by an Ansible
  `lookup('password', ...)` call rooted at
  `tack/.secrets/<name>` on the controller. The same lookup pattern
  produces stable values across re-runs. QA must NOT share secret
  files with prod; the QA inventory should point its lookups at a
  separate directory like `tack/.secrets-qa/`.
- Backup directory: production uses `/root/backups`. QA can use the
  same path inside its own LXC; no clash because the LXCs are
  different filesystems.
- Cloudflare tunnel name: not in scope for the QA stack today
  (production Tack does not run a Cloudflare tunnel; access is via WG).
  Open question in section 9.

### 4.3 QA env file shape

The QA `.env` file has the same variable names as production (section
1.2), with QA-specific values. Variable names only, no values:

```
YUGABYTE_USER
YUGABYTE_PASSWORD
YUGABYTE_DB
MEILI_MASTER_KEY
AUDIT_WRITER_DSN
AUDIT_READER_DSN
AUDIT_REDACTOR_DSN
AUDIT_WRITER_PASSWORD
AUDIT_READER_PASSWORD
AUDIT_REDACTOR_PASSWORD
AUDIT_SIGNING_KEY_PATH
LOG_LEVEL
LOG_MAX_BACKUPS
LOG_MAX_AGE_DAYS
LOG_MAX_SIZE_MB
FDB_VERSION
PORT
```

Storage: the operator's secret store is the gitignored
`tack/.secrets-qa/` directory on the Ansible controller. The Ansible
`password` lookup writes new files there on first run and reuses them
on later runs. The directory is never committed to the configs repo;
loss recovery is a fresh QA secret rotation.

The new template file, `tack/tack-qa.env.j2`, needs to mirror the
production env shape rather than the stale single-DB shape currently
in `tack/tack.env.j2`. A cleaner refactor is to replace
`tack/tack.env.j2` with a real template that matches production and
parameterize the env-file path by group, so prod and QA share one
template fed by different group_vars.

### 4.4 Inventory entries

In `/Users/agoodkind/Sites/configs/ansible/inventory/hosts`:

```
[tack_servers]
tack.home.goodkind.io ansible_host=3d06:bad:b01::117

[tack_qa_servers]
tack-qa ansible_host=3d06:bad:b01:200:1::117
```

If the operator chooses to give QA an FQDN, replace `tack-qa` with
`tack-qa.home.goodkind.io` (or `tack-qa.qa.goodkind.io`).

In `/Users/agoodkind/Sites/configs/ansible/inventory/group_vars/`:

- New file `tack_qa_servers.yml`: clones `tack_servers.yml` with QA
  values: `tack_install_dir: /root/tack`, `tack_env: qa`,
  secret lookup paths pointing at `tack/.secrets-qa/`, and the IPv6
  address override.
- Add a `tack` entry under `service_mapping` in
  `group_vars/all/service_mapping.yml` for the QA host:
  `tack_qa: { hostname: tack-qa, ipv6: "3d06:bad:b01:200:1::117" }`.

### 4.5 Conditional role tasks

The playbook needs a few env-conditional behaviors:

- The host-level setup (sysctl, ndppd, `/etc/docker/daemon.json`,
  `/etc/tack/audit-signing.pem`) is currently NOT in the playbook at
  all. Production has it because it was installed by hand. The QA
  build is the right time to lift those into the playbook. Make them
  unconditional: every Tack host needs them.
- The IPAM `/96` carve for the Docker bridge must be different per
  host (production uses `3d06:bad:b01:0:7ac::/96`; QA uses something
  like `3d06:bad:b01:200:1:7ac::/96`). Surface this as
  `tack_docker_v6_subnet` in group_vars and template the Compose file
  with it.
- The ndppd rule must match the IPAM subnet; surface this as
  `tack_ndp_proxy_prefix` in group_vars.
- The audit signing key path is the same on both hosts; the key file
  itself is per-host (and must NOT be reused across environments).

### 4.6 Deploy invocation

Once the playbook is adapted:

```bash
cd /Users/agoodkind/Sites/configs/ansible
ansible-playbook \
  -i inventory/proxmox.yml -i inventory/hosts \
  playbooks/deploy-tack.yml \
  --limit tack_qa_servers
```

The `--limit` selects only the QA group. The playbook should be
idempotent: re-running it on the same host produces the same result.

The Compose stack `up -d` step should be folded into a `compose-up`
task that handles the prod-style "build from local source" path. The
cleanest division is:

1. Ansible owns host setup, env file rendering, and `docker compose up
   -d` of an externally built image.
2. `make deploy` (or a future `make deploy-qa`) owns the rsync of the
   Tack source tree and the in-place `docker build`.

For TACK-244, the goal is item 1; item 2 is straightforward and
already exists. Wiring `make deploy-qa` to point at `tack-qa` is a
small change in `Makefile` (a parametrized SSH host) that the
execution agent can do alongside the playbook work.

---

## Section 5: data-gen script design (TACK-245 input)

### 5.1 Where it lives

A new subcommand under the existing `./server ops` family:
`./server ops qa-datagen`. The ops registry pattern is established at
`internal/ops/ops.go:90` (`ops.Register`); each op lives in its own
file under `internal/ops/`. The data-gen tool fits the same shape as
`backfill_default_children`, `backfill_addresses_apply`, etc.

Reasons to put it under `ops`:

- The "spirit-of-monolith" decision: Tack ships one binary, and ops
  commands are the first-class extension point.
- Ops commands already share the FDB and Yugabyte clients via
  `*config.Config`, so no new wiring.
- The data-gen tool MUST go through the same service-layer validators
  as production writes (section 5.6). Living inside the binary makes
  that mechanical: `import "goodkind.io/tack/internal/service"` and
  call `NodeService.CreateNode` directly.

Concretely: a new file `internal/ops/qa_datagen.go` with an `init()`
that calls `ops.Register(ops.Operation{Name: "qa-datagen", Run: ...})`,
plus supporting files for the data shapes.

### 5.2 Data shapes generated

The tool must exercise every Tack code path. That decomposes into:

- **Organizations**: small (one workspace, one project, ~10 issues),
  medium (3 workspaces, 5 projects each, ~100 issues per project), and
  large (5 workspaces, 10 projects, ~1000 issues per project, deep
  hierarchy). Each size is a flag.
- **Node types**: every type registered in the type system. At minimum:
  org, workspace, project, state, label, issue, epic, cycle, module,
  comment, activity. Also: at least one custom user-defined node type
  to verify the type-extension path.
- **Relationships**: assignments (issue -> user), containment (issue
  in project, project in workspace), labels-on-nodes, parent_id
  hierarchy (issue under epic, epic under cycle), epic_of edges,
  watchers, custom relationship types from CanContain metadata.
- **Property types**: every primitive (string, number, bool,
  date, uuid_ref, enum, json), with edge-case values: empty string,
  unicode (including emoji and RTL), max-length string (10 KB),
  null, deeply nested JSON, very small and very large numbers,
  timezone-aware and naive dates, zero values, negative numbers.
- **Audit verbs**: at least one audit event for each verb the
  audit recorder emits (`node.create`, `node.update`, `node.delete`,
  `node.move`, `relationship.create`, `relationship.delete`,
  `property.set`, etc.). The data-gen tool drives these by performing
  real CRUD operations through the service layer; the audit recorder
  fires on its own.
- **Address shapes**: every `addressKind` known to the address
  resolver. Slugs, sequence references like `TACK-65`, scoped labels
  like `TACK::In Progress`, UUID references.
- **Users and orgs**: at least three users per org, at least two
  orgs per user. This catches the "user is a member of multiple orgs"
  cases that single-tenant tools often miss.

### 5.3 Idempotency

Suggested strategy: deterministic UUIDv5 derivation from a seeded
namespace plus a logical name. Re-runs of the tool are no-ops on
already-existing nodes. Tradeoffs:

- Pro: re-running after a partial failure resumes cleanly. No "delete
  the QA dataset and start over" cycles.
- Pro: deterministic IDs make assertions in QA test scripts stable.
- Con: deterministic IDs do not exercise the UUIDv7 generation path
  that production uses. The tool should generate a mix: a small
  deterministic core (orgs, workspaces, seed users) and a larger
  random tail (issues, comments) seeded by a `--seed` flag for
  reproducibility but using UUIDv7 from a deterministic clock.
- Con: idempotency only at the node level. Updates and deletes are
  inherently non-idempotent; the tool needs an explicit `--mode`
  flag with values `create-only`, `mutate`, `full` so the operator
  can choose.

Concrete recommendation: `--mode create-only` on first run, then
`--mode mutate` to drive a second wave of updates and deletes that
exercise the audit verbs. The `mutate` mode generates a fresh suffix
per run to avoid replaying the same delete twice.

### 5.4 Scale parameters

Flags on `./server ops qa-datagen`:

- `--size {small,medium,large}`: presets defined above.
- `--orgs N`, `--workspaces-per-org N`, `--projects-per-workspace N`,
  `--issues-per-project N`: explicit overrides.
- `--seed UINT64`: PRNG seed for any random data (issue titles,
  comment bodies, property values).
- `--mode {create-only,mutate,full}`: see 5.3.
- `--dry-run`: log what would be created without writing. Useful for
  reviewing the plan against `internal/service/node_props.go`
  validators before committing to the write.
- `--rate-limit-qps INT`: cap creation rate. Defaults high; useful
  when QA shares a host with other workloads.

The fast path for iteration: `./server ops qa-datagen --size small`
should complete in under 30 seconds. The realistic-load path:
`--size large` should produce ~50 GB of FDB data in about an hour.

### 5.5 Relationship to seed

The seed pass at `cmd/server/seed.go` is currently disabled (TACK-230
guard). It produces the bootstrap user, org, and workspace from
`SEED_EMAIL` and `SEED_NAME` env vars.

Proposed division of labor:

- `./server seed` continues to own the bootstrap: one user, one org,
  one workspace, the deterministic seed admin. It is the precondition
  for any real auth.
- `./server ops qa-datagen` runs AFTER seed. It expects the seed user
  to exist and creates additional users, orgs, workspaces, and all
  data on top.

The data-gen tool calls into `service.NodeService` and never touches
the seed-only code paths. If seed is still disabled by the TACK-230
guard, data-gen must be wired to a fallback: an opt-in
`--bootstrap` flag that creates a minimal admin user and org before
running its main pass. This avoids gating QA on the prod seed fix.

### 5.6 Relationship to the validator

Every write path in the data-gen tool MUST go through
`internal/service/NodeService.CreateNode` (or the equivalent service
method for the entity type). That method calls
`validateCreateProps` at `internal/service/node_props.go:18`, which
checks the property values against the declared `PropertyDef` list
for the org. This is the same path production writes take.

What this rules out: directly calling `EntityRepository.CreateAtomic`
from data-gen. The repo path bypasses validation, and any data-gen
that uses it would produce data shapes that production writes could
never produce. That defeats the purpose of QA.

The data-gen tool's per-node creation function is a thin wrapper
around the service-layer CreateNode call, with property values filled
in from typed generators (one per `PropertyDef.Type`).

---

## Section 6: end-to-end QA build sequence

The expected ordered steps from a clean state. Each step lists
expected duration, verification, and rollback.

### Step 1: operator setup (5 minutes, manual)

- Generate a stable MAC for the QA LXC (e.g. `BC:24:11:A3:53:17`).
- Pick the IPv6 address (`3d06:bad:b01:200:1::117/64`).
- Pick the Docker IPAM `/96` (`3d06:bad:b01:200:1:7ac::/96`).
- Ensure WireGuard from the laptop to suburban is up: `ssh
  root@10.240.0.148 'echo ok'`.
- Verification: `ssh root@10.240.0.148` succeeds.
- Rollback: none required.

### Step 2: Tofu apply (3-5 minutes)

- `cd /Users/agoodkind/Sites/configs/opentofu`.
- `tofu plan -target=proxmox_virtual_environment_container.tack_qa`,
  review the plan.
- `tofu apply -target=proxmox_virtual_environment_container.tack_qa`.
- Verification: `ssh root@10.240.0.148 'pct status 103'` reports
  running. `ssh root@10.240.0.148 'pct exec 103 -- ip -6 addr show
  eth0'` shows the assigned address.
- Rollback: flip `prevent_destroy = false`, `tofu destroy -target=...`.

### Step 3: Ansible playbook run (10-15 minutes first run)

- `cd /Users/agoodkind/Sites/configs/ansible`.
- `ansible-playbook -i inventory/proxmox.yml -i inventory/hosts
  playbooks/deploy-tack.yml --limit tack_qa_servers`.
- This installs Docker, sets up sysctl/ndppd/`daemon.json`, generates
  the audit signing key, renders the env file, and copies the Compose
  file.
- Verification: `ssh tack-qa 'docker ps'` shows the six services.
  `ssh tack-qa 'docker logs tack-app-1 2>&1 | tail -20'` shows
  startup with no errors.
- Rollback: `ansible-playbook ... --tags wipe` once a wipe play is
  added (planned in section 8). Manual fallback: `ssh tack-qa 'cd
  /root/tack && docker compose down -v'`.

### Step 4: Compose stack up (1-2 minutes)

This is folded into step 3 because the playbook ends with `docker
compose up -d`. Listed separately to make the verification gate
explicit.

- Verification: each healthcheck passes within `start_period`. The
  `app` container reaches `service_healthy` within 60 seconds of
  starting.

### Step 5: migrations apply (5-30 seconds)

- `ssh tack-qa 'cd /root/tack && docker compose exec app /server
  migrate'`.
- Verification: command exits 0. `psql` connection to Yugabyte shows
  `audit.events`, `users`, `api_tokens`, `org_members` tables.
- Rollback: drop and recreate the Yugabyte volume, re-run.

### Step 6: data-gen run (30 seconds to 1 hour, depending on size)

- `ssh tack-qa 'cd /root/tack && docker compose exec app /server ops
  qa-datagen --size medium --mode create-only --seed 1'`.
- Verification: command exits 0. The summary line reports the count
  of nodes created per type. Subsequent
  `./server ops qa-datagen --size medium --mode mutate --seed 1`
  produces a non-zero update count.
- Rollback: `./server ops qa-datagen --teardown` (a flag the tool
  must support). Last-resort rollback: clear the FDB and Yugabyte
  volumes and rebuild from step 4.

### Step 7: QA smoke check (1-2 minutes)

A short script that exercises the API:

- API up: `curl -s -6 'http://[3d06:bad:b01:200:1::117]:8000/healthz'`
  returns 200.
- Sample read: an MCP request for `tack_list_workspaces` against
  the QA bearer token returns the seeded workspaces.
- Sample write: an MCP request for `tack_create_issue` with a
  validating payload succeeds, and a follow-up `tack_get_issue`
  returns the new issue.
- Audit: `./server ops audit query --workspace=...` returns the
  freshly written events.

If all four pass, QA is up. Total elapsed time from a clean state
is roughly 20 minutes for `--size small` data-gen.

---

## Section 7: refresh procedure

The recurring operation that keeps QA realistic: refreshing the QA
dataset from a real production snapshot.

### 7.1 Source

Production backups land at `/root/backups/tack-<TS>/` on CT 117. Each
snapshot directory contains:

- `MANIFEST.txt`: SHA-256 plus size for every file in the snapshot.
- `fdb/fdbbackup.tar.gz`: an `fdbbackup` snapshot of the FDB cluster.
- `fdb/backup_url.txt` and `fdb/describe.txt`: backup metadata.
- `meilisearch/tack_meili-data.tar.gz`: Meilisearch data snapshot.
- `temporal-db/temporal.sql`: Temporal Postgres dump.
- `yugabyte/tack.sql`: full Yugabyte logical dump (1.1 GB at probe
  time).

The latest snapshot at probe time is
`/root/backups/tack-20260509T232955Z`.

### 7.2 Transport

WireGuard direct rsync is the simplest transport: tack and suburban
are both reachable on `3d06:bad:b01::/56` from each other via the
laptop's WG, and tack can SSH directly to suburban via the suburban
host's IPv6 address (assuming SSH key trust is set up). The single
command shape:

```
ssh tack 'rsync -az --progress /root/backups/tack-<TS>/ \
  root@10.240.0.148:/var/lib/vz/snippets/tack-qa-restore/'
```

A 1.1 GB Yugabyte dump plus 13 MB Meili plus 1 MB FDB transfers in
under 5 minutes over a 100 Mbps WireGuard link.

A Cloudflare R2 stage is overkill for the LAN/WG case and adds a
cost surface that is not needed. R2 becomes interesting only if QA
moves off the WG-reachable network, which is not in the current plan.

### 7.3 Redaction

PII in the Tack data model lives in:

- User identity in Yugabyte: `users.email`, `users.display_name`,
  `users.avatar_url`.
- Audit PII: `audit.pii.payload` (encrypted) and audit `actor_id`s.
- Node properties in FDB: `name` and `description` on issues, comments,
  activities, and any custom node type. Comments and descriptions
  carry the most free-form PII risk.
- Audit ledger references: the audit verb `actor_id` ties events back
  to user UUIDs, which are themselves derived from email at seed
  time.

Recommendation: redact during restore, not during snapshot creation
or transport. Snapshot creation must stay a single-shot atomic
operation, and transport over WireGuard is already on a private link.
Restore is the natural place because the operator can review what
the redactor would change before committing.

A new ops command is the right shape:
`./server ops qa-redact --backup-dir <path>`. It rewrites the dump
files in place by:

- Replacing `users.email` with `user-<uuid>@qa.invalid`.
- Replacing `users.display_name` with `QA User <short-uuid>`.
- Replacing `audit.pii.payload` with NULL.
- Walking FDB `node_list_view` records and rewriting `name` and
  `description` for any `NodeType` flagged as carrying PII (issue,
  comment, activity, and any custom type marked
  `Features: ["pii"]`).
- Preserving `audit.chain_heads`. The audit hash chain must not be
  broken; redactors zero the payload but keep the event structure.

The redactor MUST run before the `restore` step. The full flow is
`backup -> transport -> redact -> restore -> data-gen top-up`.

### 7.4 Frequency

Recommend on-demand, not scheduled. Reasons:

- Production snapshots run nightly already; the cost of a refresh is
  in the human review of the redacted data, not the CPU.
- Scheduled refresh leads to QA drift relative to whatever
  in-progress test the operator is mid-way through.
- A weekly refresh as a soft convention works better as a calendar
  reminder than as a cron job.

### 7.5 Locking

A simple file lock on the QA host. The refresh script touches
`/root/.qa-refresh.lock` at start and removes it at the end. Two
concurrent refreshes would attempt to lock; the second exits with a
clear message. A `flock(1)` wrapper around the script does this in
one line.

If a stale lock blocks a legitimate refresh, the operator runs `ssh
tack-qa 'rm /root/.qa-refresh.lock'` after confirming no process
holds it.

---

## Section 8: rollback and teardown

### 8.1 Clean teardown of QA

This is the reverse of section 6:

1. `ssh tack-qa 'cd /root/tack && docker compose down -v'` (drops the
   stack and its volumes).
2. Flip `prevent_destroy = false` on the Tofu QA resource.
3. `tofu destroy -target=proxmox_virtual_environment_container.tack_qa`.
4. Remove `tack_qa_servers` from `inventory/hosts` (optional; leaving
   it lets a fresh build come back faster).

Total elapsed: 5-10 minutes.

### 8.2 Wipe data without rebuilding the LXC

When the operator wants to keep the QA host but reset the data:

```
ssh tack-qa 'cd /root/tack && docker compose down -v && docker compose up -d'
ssh tack-qa 'cd /root/tack && docker compose exec app /server migrate'
ssh tack-qa 'cd /root/tack && docker compose exec app /server ops qa-datagen --size medium'
```

This drops every named volume (Yugabyte, FDB, Meili, Temporal, logs,
audit WAL), brings the stack back up clean, runs migrations, and
re-fills with synthetic data. About 5 minutes total.

### 8.3 Hot-restart without losing data

When the operator wants to bounce the stack but keep the dataset:

```
ssh tack-qa 'cd /root/tack && docker compose restart'
```

Or, for a single service: `docker compose restart app`. Volumes are
untouched. Recovery time depends on Yugabyte's startup, typically
30-60 seconds before all healthchecks pass.

Avoid `docker compose down` without `-v` against this stack: it works
in principle but `down` plus `up` exercises the Yugabyte
`--base_dir` recovery path, and historical bugs in that path are why
the wrapper script in section 1.1 sets `--base_dir` explicitly.
`restart` is safer.

---

## Section 9: open questions

The questions below are the decisions the planner could not make
confidently from inspection. Each is one or two paragraphs covering
what the question is, why it matters, candidate answers, and a
recommendation if there is one.

### 9.1 vmbr0 versus vmbr1 for the QA LXC

The QA LXC could attach to `vmbr1` (the suburban VM segment,
`3d06:bad:b01:200::/64`) or `vmbr0` (the Comcast NJ LAN bridge with
public IPv6 SLAAC). On `vmbr1` the host is reachable only via the
WireGuard tunnel and is invisible to the public internet. On `vmbr0`
the host has direct upstream reachability but is exposed to anything
the upstream Comcast assignment lets through.

The recommendation is `vmbr1`. QA does not need public reachability;
the operator and any internal data-gen runs reach it over WireGuard;
audit ledger isolation is improved by keeping it off the public path;
and section 2's IPv6 plan already targets the suburban VM segment.

The risk of `vmbr1`: any laptop without WireGuard cannot reach QA.
That is a feature, not a bug, but it does mean the operator needs WG
up before running migrations or data-gen.

### 9.2 DNS entry for tack-qa

The QA host could have an entry in `service_mapping`
(`tack-qa.home.goodkind.io`) and a corresponding BIND record under
`goodkind.io`, or it could be reachable purely by IPv6 with an SSH
config alias.

The recommendation is the SSH alias plus a `service_mapping` entry,
without a public BIND record. The alias gives the operator
`ssh tack-qa` ergonomics; the `service_mapping` entry keeps the
ansible/configs repos consistent; and avoiding a public DNS record
keeps QA deniable from the open internet, which mirrors the audit
isolation argument from 9.1.

### 9.3 Cloudflare tunnel for QA

The production Tack host does NOT run a Cloudflare tunnel today;
access is via WireGuard. QA could either match production (no
tunnel) or run its own tunnel for external operator access.

The recommendation is no tunnel. The cost surface is real (a tunnel
is one more thing to operate) and the access path (WG to suburban,
then IPv6 to QA) is already healthy. If the operator later wants
external QA reach for collaborators, a separate Cloudflare tunnel
makes more sense than adding it now.

### 9.4 Where does the redaction step happen

Section 7 recommends "during restore", but "during snapshot
creation" and "during transport" are also defensible. Snapshot-time
redaction has the strongest privacy story (PII never leaves
production) but ties redaction logic to the production backup path
and complicates restoring a redacted snapshot back to production in
a real DR scenario. Transport-time redaction is awkward because the
transport is rsync, not a programmable channel.

Recommendation: restore-time, with the production snapshot kept
unredacted on tack and only the QA copy redacted. This keeps
production DR simple and confines redaction to the QA path.

A counterargument worth flagging: regulators often prefer that PII
never crosses a security boundary, even a WG one. If the operator
later moves QA to a cloud host, snapshot-time redaction becomes the
right answer and the implementation should be designed to support
either path.

### 9.5 Fresh QA versus extending an existing dataset for data-gen

Two modes are possible: data-gen always starts from an empty stack,
or data-gen can layer onto an existing dataset (whether seeded or
restored from production).

The recommendation is to support both. The `--mode create-only` flag
covers fresh dataset creation; the `--mode mutate` flag covers
top-up against an existing dataset. The redaction-then-data-gen-top-up
flow in section 7 needs the second mode. Single-mode tools that only
work fresh make refresh unworkable.

### 9.6 Audit ledger sharing

QA must not share the audit notarizer key with production. Sharing
the key would let a QA operator forge production-signed audit events,
which collapses the audit ledger's compliance claim entirely.

Recommendation, stated explicitly: QA generates its own
`/etc/tack/audit-signing.pem` at first deploy. The Ansible task that
generates the key is unconditional and per-host, which guarantees
this. The key file is NOT in git, NOT shared with production, and is
backed up only as part of the QA host backup (which itself is
optional because QA is rebuildable from prod plus data-gen).

This question is closed; calling it out keeps the answer durable
across plan revisions.

### 9.7 FDB version pinning across environments

Production pins `FDB_VERSION=7.4.6`. QA must match. Drift here is a
silent class of bugs because the FDB Go bindings carry an API version
that must align with the cluster.

Recommendation: surface `FDB_VERSION` in the QA env file and assert
in the playbook that the running cluster reports the same version
the env file declares. A simple `fdbcli --exec 'status' | grep
'^Cluster:'` check after stack startup is enough.

---

End of plan. The next step is for the operator to review section 9
answers, accept or override each, and hand the resulting tickets to
execution agents.
