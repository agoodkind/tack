# Search Cluster Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define one initial OpenSearch guest and one stable search endpoint in each environment while preserving later production scale-out.

**Architecture:** Configs provisions LXC guests, TLS, credentials, inventory, and one health-checking Traefik service on each hypervisor. Tack defines the pinned container. The existing audited provision and verify commands use the stable endpoint. Production starts as a normal one-member cluster so later members can join without changing Tack.

**Tech Stack:** OpenTofu, Proxmox LXC, Ansible, Traefik 3.0, Docker Compose, OpenSearch 3.8.0.

**Spec:** [Deployment and capacity](../specs/2026-09-19-search-design.md#deployment-and-capacity).

## Global Constraints

Apply the [implementation constraints](2026-09-19-opensearch.md#global-constraints). Perform this Luna task only inside the selected Tack and Configs worktrees. The only permitted external writes are the documented branch and pull-request publication operations. Require `CONFIGS_ROOT` to identify a clean isolated Configs checkout based on `origin/main`. Run only `make build`, the documented RSpec render tests, and `./configsctl tofu validate`. Do not invoke Docker, Ansible, `configsctl deploy`, an OpenTofu plan or apply, SSH, a service request, a remote API probe, a hypervisor API, or any command that inspects or changes QA, production, another host, a container runtime, or infrastructure. QA starts on suburban with zero replicas. Production starts on vault with zero replicas. Each guest has at least 8 GiB memory, 2 CPU cores, 40 GiB fast storage, and a 2 GiB JVM heap. This task prepares configuration only. It does not deploy.

## Review Focus

Test stable endpoints, verified TLS, IPv6-only connections, single-node restart, model placement, later member joining, proxy distribution, replica placement, and QA host memory.

---

### Task 1: Define containers and initial guests

**Files:**

- Modify: Tack `docker-compose.yml`
- Test: Tack `internal/test/integration/search_cluster_test.go`
- Modify: configs `ansible/inventory/group_vars/all/service_mapping.yml`
- Create: configs `ansible/inventory/group_vars/tack_search.yml`
- Modify: configs `ansible/inventory/group_vars/tack_prod_all.yml`
- Modify: configs `ansible/inventory/group_vars/tack_qa_all.yml`
- Modify: configs `ansible/inventory/group_vars/proxmox_servers.yml`
- Modify: configs `ansible/inventory/group_vars/vault_servers.yml`
- Modify: configs `ansible/inventory/group_vars/suburban_servers.yml`
- Modify: configs `ansible/playbooks/deploy-tack.yml`
- Modify: configs `ansible/playbooks/deploy-proxmox.yml`
- Create: configs `ansible/playbooks/tasks/tack-search-proxy.yml`
- Create: configs `proxmox/config/tack-search-proxy.yml.j2`
- Create: configs `proxmox/services/tack-search-proxy.service.j2`
- Create: configs `opentofu/vault/tack_search.tf`
- Create: configs `opentofu/suburban/tack_search_qa.tf`
- Test: configs `spec/ansible/tack_search_spec.rb`
- Test: configs `spec/ansible/tack_search_proxy_spec.rb`

**Interfaces:**

- This plan requires the pinned image, model identity, mapping, official client, and registered `ops search provision` and `ops search verify` commands.
- This plan implements one HTTPS endpoint per environment, one backend initially, and reusable member lists for final validation and release.

```ruby
def production_inventory(member_count)
  inventory = deep_copy(base_production_inventory)
  inventory["tack_search_members"] = (1..member_count).map { |number| "tack_search#{number}" }
  inventory
end
```

- [ ] **Step 1: Add failing render tests for one and three members.**

```ruby
it "keeps one application endpoint while production members grow" do
  one = AnsibleRender.render("tack.env.j2", inventory: production_inventory(1))
  three = AnsibleRender.render("tack.env.j2", inventory: production_inventory(3))
  expect(one.fetch("OPENSEARCH_URLS")).to eq(three.fetch("OPENSEARCH_URLS"))
  proxy = AnsibleRender.render("tack-search-proxy.yml.j2", inventory: production_inventory(3))
  expect(proxy.scan(/url: https:\/\/tack-search\d+:9200/).length).to eq(3)
end
```

- [ ] **Step 2: Run the render tests and record the missing-template failure.**

Run: `bundle exec rspec spec/ansible/tack_search_spec.rb spec/ansible/tack_search_proxy_spec.rb`

Expected: FAIL because the search inventory and proxy templates do not exist.

- [ ] **Step 3: Reserve the two initial guest identities.**

Inspect the complete committed service mapping and Proxmox inventory in the Configs worktree. Add one production `tack_search1` entry and one QA entry with `_suburban`. Use a QA VMID equal to the production VMID plus 100 only when both values are unused in the committed inventory. Allocate distinct IPv6 addresses, pinned MACs, and Docker IPv6 subnets that are unused in the committed inventory. Keep `tack_data1/2/3` unchanged. Do not reserve later production guests. Add live collision verification as a checklist item in the release plan before any OpenTofu plan or apply. Do not query Proxmox or another host during this task.

- [ ] **Step 4: Define guest resources and host prerequisites.**

Use the existing bridge, gateway, DNS, Debian template, unprivileged nesting, discard, and `prevent_destroy` patterns. Set memory to 8192 MiB, cores to 2, and fast-pool disk to 40 GiB. Define the OpenSearch memory-map prerequisite through Configs. Require the release plan to verify the `vm.max_map_count` setting inside the LXC before container start. Do not execute that verification during this task.

- [ ] **Step 5: Render the pinned role-capable container.**

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
      nofile: {soft: 65536, hard: 65536}
```

QA uses `discovery.type: single-node`. Production uses `discovery.seed_hosts` from the member list and sets `cluster.initial_cluster_manager_nodes` only for the first formation of `tack-search1`. Remove that bootstrap setting after cluster formation. Later members discover an existing member.

- [ ] **Step 6: Define one verified hypervisor endpoint.**

Do not run `deploy-proxmox.yml` during this task. The playbook installs pinned Traefik during an authorized release. Listen on `service_mapping.vault_hypervisor.ipv6:9200` for production and `service_mapping.vmbrtrunk_suburban.ipv6:9200` for QA. Restrict callers to Tack application guests. Terminate verified client TLS, re-encrypt to each backend, verify the search CA, and use an authenticated cluster-health request for readiness. Keep credentials root-only with `no_log`.

- [ ] **Step 7: Add least-load model placement and replica rules.**

Set `plugins.ml_commons.only_run_on_ml_node: true`, least-load dispatch, and automatic redeployment. Configure the audited provisioning operation to deploy the pinned model without `node_ids`. Require `DEPLOYED` on each eligible ML node during release validation. Configure both environments with zero replicas. Require distinct guests before setting one replica. Do not deploy the model during this task.

- [ ] **Step 8: Connect the audited provisioning commands.**

Render the stable endpoint, CA path, credentials, shard counts, routing-shard count, and replica count consumed by the registered `ops search provision` and `ops search verify` commands. Render `OPENSEARCH_PUBLIC_ENABLED=false` for the initial QA and production configuration. Provision applies the validated replica count through the typed client, waits for green, records it with the physical generation, and reconciles identical retries. Verify checks image, model, mapping, shards, routing shards, replicas, TLS, and alias.

- [ ] **Step 9: Author real single-node and scale-out tests.**

The tests send every client request through real Traefik and require `ConnectionObserver` to record only the stable endpoint. They stop the node while FDB writes commit, then restart it and require backlog recovery. The production scale-out case joins two members, adds proxy backends, deploys the model, sets one replica, stops each member, and requires search and indexing to continue. The initial OpenTofu plan still creates one production guest. The final validation plan executes the disposable cases.

- [ ] **Step 10: Encode the QA capacity gate.**

Add the 6.26 GiB minimum available-memory threshold, 40 GiB fast-pool allocation, two-vCPU allocation, and single-node QA restriction to the rendered configuration tests. The final validation plan measures the live suburban host during the complete workload.

- [ ] **Step 11: Run offline configuration checks.**

Run in Tack:

```sh
make build
```

Run in configs:

```sh
bundle exec rspec spec/ansible/tack_search_spec.rb spec/ansible/tack_search_proxy_spec.rb
./configsctl tofu validate
```

Do not connect to Proxmox, start OpenSearch, or save an apply plan. The final validation plan runs disposable cluster tests. The release plan saves and reviews live OpenTofu plans.

- [ ] **Step 12: Create the Tack stack tip and the independent Configs pull request.**

```sh
git add docker-compose.yml internal/test/integration/search_cluster_test.go
```

Run Graphite MCP `create` from stack position 7 with this exact message:

```text
Define the pinned OpenSearch container

Co-authored-by: Codex <noreply@openai.com>
```

This branch is stack position 8.

```sh
git add ansible/inventory/group_vars/all/service_mapping.yml ansible/inventory/group_vars/tack_search.yml ansible/inventory/group_vars/tack_prod_all.yml ansible/inventory/group_vars/tack_qa_all.yml ansible/inventory/group_vars/proxmox_servers.yml ansible/inventory/group_vars/vault_servers.yml ansible/inventory/group_vars/suburban_servers.yml ansible/playbooks/deploy-tack.yml ansible/playbooks/deploy-proxmox.yml ansible/playbooks/tasks/tack-search-proxy.yml opentofu/vault/tack_search.tf opentofu/suburban/tack_search_qa.tf proxmox/config/tack-search-proxy.yml.j2 proxmox/services/tack-search-proxy.service.j2 tack/tack.env.j2 tack/docker-compose.override.yml.j2 spec/ansible/tack_search_spec.rb spec/ansible/tack_search_proxy_spec.rb
git commit -S -m "Add initial QA and production Tack search guests" -m "Co-authored-by: Codex <noreply@openai.com>"
```

Create the Configs commit from the current remote trunk and publish it through the normal `pr` workflow. It is independent of the Tack Graphite stack.
