# Search Cluster Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prepare one search guest in each environment and preserve production scale-out without changing Tack or regenerating embeddings.

**Architecture:** Configs provisions guests, networking, credentials, certificates, and one health-checking Traefik endpoint on each hypervisor. Tack defines the containers and search setup. Both environments validate application behavior and single-node recovery. Production uses normal cluster discovery from its first start, so later members can join behind the same endpoint.

**Tech Stack:** OpenTofu, Proxmox LXC, Ansible, Traefik 3.0, Docker Compose, OpenSearch 3.8.0, and official OpenSearch Go client v4.7.3.

**Spec:** [Deployment and capacity](../specs/2026-09-19-search-design.md#deployment-and-capacity).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). QA starts with `tack-search1` on suburban and zero replicas. Production starts with `tack-search1` on vault and zero replicas. Each guest has at least 8 GiB memory, 2 CPU cores, 40 GiB storage, and a 2 GiB JVM heap. The same inventory and container template support any number of combined, ML-only, data-only, and coordinating-only roles. Production must use normal cluster discovery from its first start. This task prepares changes and evidence; deployment requires separate authorization.

Final rendered and live environments contain no Meilisearch service, endpoint,
secret, volume, dependency, or fallback. FoundationDB supplies every initial
OpenSearch rebuild. No deployment operation reads or converts the Meilisearch index.

Task 11 prepares role-specific OpenSearch services and guest configuration. It does
not deploy them or change the active application search path. Tasks 7 and 8 own the
only application cutover. Task 12 provisions an empty OpenSearch index, deploys that
cutover, rebuilds from FoundationDB, verifies public search, and removes the unused
Meilisearch deployment. No step writes to both engines.

## Review Focus

Test the stable endpoint, single-node outage recovery in each environment, later cluster joining and replica placement, model availability after restart, cross-process Tack continuation, verified TLS, IPv6-only connectivity, and concurrent rebuilding within resource limits.

---

## Task 11: Define containers and prepare the two initial guests

Tack changes:

```text
docker-compose.yml                                OpenSearch service definition
internal/ops/search_provision.go                   audited model/index provisioning
internal/ops/search_verify.go                      deployed search verification
internal/test/integration/search_cluster_test.go   real QA and production topology cases
```

Configs changes, relative to its repository root:

```text
ansible/inventory/group_vars/all/service_mapping.yml guest identities and groups
ansible/inventory/group_vars/tack_search.yml          search guest settings
ansible/inventory/group_vars/tack_prod_all.yml        production endpoints
ansible/inventory/group_vars/tack_qa_all.yml          QA endpoints
ansible/inventory/group_vars/proxmox_servers.yml      shared proxy settings
ansible/inventory/group_vars/vault_servers.yml        production listener
ansible/inventory/group_vars/suburban_servers.yml     QA listener
ansible/playbooks/deploy-tack.yml                    role-specific service startup
ansible/playbooks/tasks/tack-search-proxy.yml        hypervisor proxy deployment
opentofu/vault/tack_search.tf                        production LXCs
opentofu/suburban/tack_search_qa.tf                   QA LXC
proxmox/config/tack-search-proxy.yml.j2              proxy routes and health checks
proxmox/services/tack-search-proxy.service.j2        supervised proxy process
tack/tack.env.j2                                    application search environment
tack/docker-compose.override.yml.j2                 guest-specific container settings
spec/ansible/tack_search_spec.rb                     real template rendering
spec/ansible/tack_search_proxy_spec.rb               proxy rendering and service tests
```

Consume the native task's verified configuration and measured artifact checksums.
Produce two initial inventory entries, one HTTPS application endpoint per environment,
one initial proxy backend per environment, reusable member lists, and separate provisioning credentials. `ops search provision` and
`ops search verify` register through clispec and its existing audit policy. Add one `searchOpsGroup` under `opsGroup` in `internal/ops/cli_search.go`. Define two `clispec.Operation[noInput]` values and register them through `RegisterCommands`. Do not add legacy operation registration. Implement `runSearchProvision(ctx context.Context, env *Env) error` and `runSearchVerify(ctx context.Context, env *Env) error` in the new ops files.
`ops search provision` reads the desired replica count from validated environment configuration. It requires enough data nodes, applies the typed index setting, waits for green health, then records the count with the physical generation. A retry reconciles the same state.

- [ ] Add configs render tests using the existing `AnsibleRender.render` runner. Render one search guest, one application guest, and each hypervisor. Require persistent OpenSearch storage, exactly one application endpoint, and one verified backend per initial environment. Render a synthetic three-member production inventory and require three verified backends without changing the application endpoint. The application must receive no provisioning or readiness credential.
- [ ] Add the role-specific OpenSearch service and application endpoints. Keep this
  preparation inactive. Tasks 7 and 8 delete the Meilisearch application dependency,
  endpoint, and Tack service during the single cutover. Task 12 removes the remaining
  deployed secret, volume, inventory, and template values after public verification.
- [ ] Run `bundle exec rspec spec/ansible/tack_search_spec.rb`. Expect failure before the inventory and templates define search guests.
- [ ] Allocate two distinct guest IDs, addresses, pinned MACs, and Docker IPv6 subnets in service_mapping. Check the entire mapping and live guest inventory before reserving them. The existing `tack_data1/2/3` entries are ledger guests and must remain separate. Use production key `tack_search1` and one QA key with `_suburban`; use a QA VMID equal to its production counterpart plus 100 where the verified inventory permits it. Do not reserve later production guests before a reviewed scale-out change.
- [ ] Install the pinned Traefik release through `deploy-proxmox.yml`. Listen only on `service_mapping.vault_hypervisor.ipv6:9200` in production and `service_mapping.vmbrtrunk_suburban.ipv6:9200` in QA. Restrict callers to the Tack application guests. Run a separate systemd service with restart enabled.
- [ ] Terminate verified client TLS at the stable endpoint, re-encrypt to every configured OpenSearch backend, verify backend certificates with the search CA, and use an authenticated cluster-health request for readiness. Store its dedicated credential root-only through Ansible `no_log`. Keep application authorization headers intact.
- [ ] Add the LXC resources using mapping-derived identities. Match the existing production and QA bridge, gateway, DNS, Debian template, unprivileged nesting, discard, and prevent_destroy settings. Set memory to at least 8192 MiB, cores to 2, and disk size to 40 GiB. Provision one permanent search guest in each environment.
- [ ] Render the following container settings from environment-specific inventory. QA sets `discovery.type` to `single-node`. Production sets `discovery.seed_hosts` to the existing production members and bootstraps only `tack-search1` when no cluster state exists. Never set production `discovery.type` to `single-node`. Remove the bootstrap setting after the cluster first forms. Later members discover an existing member and never repeat initial bootstrap.

```yaml
services:
  opensearch:
    image: opensearchproject/opensearch:3.8.0
    environment:
      OPENSEARCH_JAVA_OPTS: -Xms2g -Xmx2g
      node.roles: cluster_manager,data,ingest,ml
    volumes:
      - opensearch-data:/usr/share/opensearch/data
    ulimits:
      nofile:
        soft: 65536
        hard: 65536
```

This fragment is the resource configuration. Add real inventory-derived discovery,
REST and transport certificate mounts, trust settings, and persistent volume
ownership in the existing templates. Reject disabled certificate verification.
Apply required host kernel settings through configs, including the OpenSearch
memory-map prerequisite; validate them inside the LXC before container startup.

- [ ] Restrict application credentials to the typed index creation and split, block, bulk, query, point-in-time, document, health, and local inference operations used by the official client. Provisioning credentials create models, semantic mappings, and aliases. The application needs no discovery permission. Verify denied unrelated administrative calls with the application identity. Use secret references and Ansible no_log for secret-bearing tasks.
- [ ] Ensure replicas cannot share a guest with their primary. Both initial environments use zero replicas. Set `plugins.ml_commons.only_run_on_ml_node` to true, use `least_load` task dispatch, and keep automatic redeployment enabled. Deploy the pinned model without `node_ids` so ML Commons selects every eligible ML node. Require `DEPLOYED` on each initial node. Store the chosen primary and reserved routing-shard counts with each physical index. Tack must not select an ML worker or shard node.
- [ ] Add one single-node integration case per environment with a real Traefik process and the production official client. `ConnectionObserver` must record only the stable endpoint. Stopping the node must make search unavailable while FoundationDB writes commit; restarting it must drain the durable backlog. Deny model-download network access after provisioning and require ordinary inference to keep working.
- [ ] Add a production scale-out integration case that starts from the normal one-member cluster, joins two members through the existing member, adds both proxy backends, waits for model deployment, then uses `ops search provision` to change replicas from zero to one. Require green health, the recorded replica count, and unchanged sparse weights for existing documents. Proxy records must show every healthy backend accepting work without exact count requirements. Stop each member in turn through the Docker SDK. Search must continue, the failed backend must leave rotation, and a newly written small node must become searchable within 10 seconds. Restart each backend and require readiness checks before it accepts work. This test validates the later operation; the initial OpenTofu plan still creates one production guest.
- [ ] Gate the QA plan on live host capacity. Suburban has 31.31 GiB usable memory, eight logical CPUs, and 215.92 GiB available fast storage. One two-vCPU guest passes the CPU projection, and one 40 GiB guest leaves 38.46 percent of the fast pool available. The workload must keep at least 6.26 GiB of host memory available. The earlier 3.4 GiB post-workload reading plus the proxy would leave about 6.69 GiB, but that reading did not establish peak use. Keep the QA guest enabled only after the complete workload passes this gate.
- [ ] Run the render tests, `tofu validate` in both OpenTofu directories, the single-node and scale-out integration cases, and repository checks. Review a saved OpenTofu plan for exactly the intended two guest additions and no unrelated replacement or deletion. Commit Tack with subject `Provision and verify the OpenSearch container cluster`; commit configs with subject `Add initial QA and production Tack search guests`.
- [ ] Do not preserve or migrate the old Meilisearch volume. Its deletion is a
  separate destructive deployment action. Request authorization after the empty
  OpenSearch index has rebuilt from FoundationDB and public search checks pass.
  Do not define a Meilisearch rollback path.

## Task 12: Verify QA and production after authorization

Consume the reviewed commits, successful native coverage report, and saved
provisioning plan. Produce deployment evidence with revisions, image digests,
model/tokenizer checksums, TLS identities, topology, and acceptance measurements.

- [ ] Present the concrete guest additions and deployment commits for authorization before applying them. Never disable a branch rule or rewrite shared history to publish these changes.
- [ ] Apply the approved QA provisioning plan to the one search guest. Create an
  empty OpenSearch index. Do not read or convert Meilisearch data.
- [ ] Deploy and verify the QA hypervisor endpoint through `./configsctl deploy deploy-proxmox --limit suburban`. Confirm its listener, certificate, backend verification, readiness checks, and source restrictions before application cutover.
- [ ] Deploy the reviewed application cutover through the existing entry point:

```sh
./configsctl deploy deploy-tack --limit tack_qa_all
```

- [ ] Generate and review the complete QA search-projection manifest. Run the
  expiring backfill in dry-run mode, execute that exact manifest, rerun it, and
  require zero changes and zero missing declarations before rebuilding.
- [ ] Keep public search unavailable until the FoundationDB rebuild activates the
  verified alias. Run audited reindexing, verification, and guarded QA datagen.
  Verify one LXC, actual resources, IPv6-only REST and transport connectivity, valid
  TLS, model identity, every primary placement, and zero replicas.
- [ ] Verify the QA application process, containers, volumes, environment, secrets,
  and inventory contain no Meilisearch resource. After search passes, present the
  exact old volume deletion for separate authorization.
- [ ] Stop the QA search guest during source writes. Require FoundationDB commits, an explicit search-unavailable error, and durable pending search work. Restart the guest and require authenticated readiness, model deployment, backlog recovery, and public search before continuing.
- [ ] Before the capacity run, record corpus size, page distribution, query mix,
  concurrency, mutation rate, rebuild activity, and pass thresholds for latency,
  errors, throughput, oldest work age, memory, and disk. Run the exact workload with
  single guest. Require disk for serving, replacement, and retiring indexes. Keep at
  least 6.26 GiB of suburban host memory available throughout the run. Do not infer
  capacity from the model bundle size or the earlier post-workload reading.
- [ ] Run two Tack processes against the same FoundationDB and OpenSearch. Alternate
  one session between them, then increase request and worker load. Add FoundationDB
  capacity independently and require improved throughput without stored-format changes.
  Keep one OpenSearch guest in the QA topology. Do not claim OpenSearch node failover
  or horizontal scale from QA evidence.
- [ ] Require all first-release acceptance checks, including multi-page behavior under today's FDB limit. Tests beyond that limit remain mandatory for the later storage change, not a reason to defer current multi-page coverage.
- [ ] After QA passes and production deployment is authorized, provision the one production search guest and deploy the production hypervisor endpoint through `./configsctl deploy deploy-proxmox --limit vault`. Keep the application on its old search configuration. Confirm the listener, certificate, backend certificate, readiness checks, source restrictions, zero replicas, one primary, reserved routing shards, and model placement.
- [ ] Before application cutover, send direct verification traffic through the stable production endpoint. Stop the production node during source writes. Require an explicit search outage while FoundationDB commits. Restart the node and require authenticated readiness, model deployment, backlog recovery, and public search.
- [ ] Use the existing production application entry point:

```sh
./configsctl deploy deploy-tack --limit tack_prod_all
```

- [ ] Repeat the manifest dry run, reviewed backfill, idempotent rerun, and
  requirement for zero missing declarations against production before its first
  OpenSearch rebuild.
- [ ] Verify deployed revisions, image/model identity, TLS, topology, and authorized smoke fixtures. Record provisioning, deployment, and live verification separately. Do not report the implementation tickets complete merely because source tests passed.
- [ ] Verify the production application process, containers, volumes, environment,
  secrets, and inventory contain no Meilisearch resource. Delete an old volume only
  after its exact destructive action has separate authorization.
- [ ] Before adding production search capacity, review the measured bottleneck and a
  concrete guest plan. Join every new member through the existing cluster without
  setting `cluster.initial_cluster_manager_nodes`. Add healthy members to the proxy,
  require model deployment on every eligible ML node, then set the reviewed replica
  count through `ops search provision` after enough data nodes join. Use the native split path only when the
  primary count needs to increase. With at least three members and one replica, stop
  each member separately and require the endpoint, every primary, local inference,
  indexing, and search to remain available before claiming failover.
