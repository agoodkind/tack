# Tack OpenTofu provider requirements

TACK-567 requires OpenTofu to manage real Tack deployment and operational state. The provider must observe deployed resources and reconcile declared configuration through authenticated, audited operations. Production adoption after QA is authorized without another approval gate. Existing production rollout must not depend on provider delivery.

## Scope and ownership

The Tack provider must own application containers, runtime configuration, audit roles, search configuration, and backup policies within existing guests. Proxmox guests, disks, network interfaces, and host configuration must remain under pveguest, overlay, and infrastructure composition. User content and tickets must not become provider resources.

Ownership must be explicit for each resource, environment, and field. Shared configuration files must have one rendering owner or a verified composition mechanism. Removing individual template variables is insufficient when an Ansible handler can recreate a provider-managed container.

TACK-568 establishes the shared model and specification. TACK-569 implements protocol 6 resource lifecycle methods. TACK-570 adopts QA application and audit-consumer resources. TACK-571 adds database, search, and backup management. TACK-572 completes Ansible ownership transfer. TACK-573 adopts production.

Each adoption stage must transfer its own fields before provider mutation. TACK-572 completes the remaining transfers and removes superseded deployment responsibilities.

## Evidence and current behavior

The current source establishes implementation behavior. Live deployment health requires separate verification. The current audit consumer has a separate executable. The server executable combines serving, migrations, seeding, audit commands, and operations.

The existing provision implementation performs database checks, migrations, audit-role seeding, continuous-backup initialization, and optional product seeding. Its seed operation can return success after deferring execution because the application container cannot accept execution. Provider readiness must require observed completion of required steps.

The existing deployment verifier reads container and image information from Docker. The provider must observe actual configuration as well as the running image. Manifest digests and local image identifiers must retain their distinct meanings during verification.

The current search provision implementation ensures an index, waits for green health, sets the alias, and records the serving index. Green health alone does not establish rebuild completion or activation acceptance.

The current Configs playbook renders configuration, starts containers, provisions stores, and installs backup responsibilities. The guest-state and deployment-controller documents establish transport and controller contracts. Their contracts do not prove that every required capability is installed on every adoption target.

## Executables and shared implementation

A separate `terraform-provider-tack` executable must use Terraform Plugin Framework protocol 6. The initial implementation must remain in the existing repository and Go module.

Keeping operator commands in the server preserves current callers during initial adoption. Further separation of server and operator executables is a recommended proposal that requires command, container-image, and deployment compatibility changes. The user has not selected that proposal. Preserve `/server` and `tack` command compatibility under either approach.

Pure deployment model, normalization, validation, and protocol types must be shared by the provider and audited Go operations. Existing command registration and operation implementations must be reused through narrow changes.


Pure model and protocol packages should avoid FoundationDB native bindings. Releases that require native libraries must publish and verify the complete runtime dependencies. Validation must not be weakened to produce a static executable.

## Initial resource contracts

`tack_service` must manage `application` and `audit_consumer` workloads. Its immutable identity must include environment and guest target. Its configuration must include an immutable image digest, configuration references, limits, mounts, and health requirements. Read must inspect the running workload and effective configuration. Delete must stop and remove only the managed workload without deleting mounted data.

`tack_audit_role` must manage a login, its grants, and credential references. Read must observe the login and effective grants. Credential rotation must require an explicit configuration change. Delete must remove the managed login or grants only after verifying dependent access requirements. Audit records must remain intact.

`tack_search_cluster` must manage membership, settings, and version within existing search guests. Read must return observed membership, configuration, version, and health. Membership changes must preserve discovery, quorum, and placement requirements. Delete must reject removal that violates verified cluster preconditions.

`tack_search_index` must manage a logical index, mapping, and model configuration. Computed state must report physical generation, rebuild progress, verification outcome, and activation status from live reads. Apply must activate a candidate only after rebuild completion and verification. Delete must respect serving aliases and existing session retirement requirements.

`tack_backup_policy` must manage schedules, retention, and target references. Read must observe installed scheduling and effective backup configuration. Policy health must remain distinct from verified restore capability. Delete must disable the managed policy without purging stored backups.

Database membership and configuration must use typed Tack store management with infrastructure composition. The migration owner must determine the exact resource decomposition from live ownership before implementation. Import must preserve cluster identity, coordinator files, seed data, volumes, and configured redundancy. Historical migration procedures remain context for [store migration constraints](../plans/2026-09-20-store-three-data-guests.md), not proof of current topology.

## Schema and state

Schemas must use typed attributes, stable maps and sets, field-level plan modifiers, explicit null and unknown handling, and versioned schema upgrades. Immutable identity changes must require replacement. Unknown required values must not become empty configuration or default mutation inputs.

Import identifiers must use the versioned format `v1/<environment>/<target>/<resource-kind>/<name>`. Each segment must use canonical percent encoding. The name must identify the workload, login, cluster, logical index, or backup policy. Database identity rules must follow the selected decomposition.

Read must derive observed values from live responses rather than copying desired configuration. A successful authoritative absence response may remove a resource from state. Authorization failures, temporary outages, stopped guests, and incomplete responses must return diagnostics without removing recorded resources.

Secrets must use references wherever possible. Sensitive attributes must suppress ordinary display. Sensitive state does not encrypt secrets. Logs, operation results, diagnostics, and audit correlation must exclude secret values.

## Typed deployment differences

Plans must show image, service configuration, resource limits, mounts, role grants, search membership, index settings, model identity, and backup-policy changes as typed attributes. JSON configuration may remain an input when shared parsing produces these attributes. Raw JSON formatting must not create deployment changes.

A configuration change that requires a restart or replacement must appear in the plan before apply. Read must report observed readiness separately from desired configuration. Provider tests must verify each replacement rule against the applicable operation contract.

## Reads, operations, and transport

Refresh and plan must perform authenticated reads only. They must not migrate, seed, rebuild, activate aliases, rotate secrets, start or stop services, or acquire environment deployment locks. Provider configuration must not provision dependencies.

Guest execution must reuse the migration owner's authenticated pveguest transport where supported. Implementation must verify the exact installed capability, privilege, execution status, and input limits. An invented endpoint or root-shell fallback does not satisfy the transport requirement.

Versioned typed requests and results must include resource identity, operation ID, observed generation, and sanitized errors. Mutation requests must also include authenticated operator identity and audit correlation. Guest execution must invoke audited Go operations through argument arrays and structured JSON. An opaque execution resource or an unquoted shell command does not satisfy explicit Create, Read, Update, Delete, and Import behavior.

Read operations and mutation operations must have separate command registrations. Mutation must resolve immutable image digests before changing a workload. Apply must reject a stale observed generation before an incompatible mutation.

Every operation must honor context cancellation and a configured timeout. A timeout after a committed mutation must return observed progress without pretending to undo the mutation.

Partial applies must recover through live progress reads and idempotent continuation. Operation IDs support correlation and recovery. The provider must not promise exactly-once execution. Deferred work must remain incomplete until a live verification establishes completion.

Destructive operations must require explicit intent and verified preconditions. Production adoption authorization does not authorize arbitrary data purging. Import must never invoke FoundationDB `configure new`, reset seeds, replace data volumes, or fabricate readiness.

## Deployment serialization

Mutations must use the existing Configs environment lock ownership and interface. Deployments within one environment must serialize across Ansible, provider operations, and other deployment requests. QA, production, and PowerEdge must make independent progress.

Terraform state locking protects state transactions. Environment deployment locking protects live deployment operations. Neither lock substitutes for the other.

The long-term architecture must use one shared OpenTofu applier queue. The provider must not introduce a competing private applier, persistent control daemon, or lock-release retry loop. The hardening owner removed the two-minute release retry and owns the underlying repair.

## Adoption and acceptance

Each resource transfer must inventory exact fields, import through read-only operations, and reconcile desired configuration to a no-change plan. The previous writer must be disabled under the same environment lock before provider mutation. Ansible and the provider must never manage the same transferred fields concurrently.

Installed-release acceptance must exercise OpenTofu initialization, validation, planning, import, and apply with real disposable dependencies. Acceptance must prove live drift detection, a no-change plan, an idempotent second apply, partial-failure recovery, mutation audit records, and zero plan mutations.

Import acceptance must prove data preservation. Temporary failures must retain resource state. Contention acceptance must prove same-environment serialization and independent-environment progress. Release acceptance must verify published artifacts, native dependencies, provider startup, and provider shutdown.

Search activation requires a search-only capacity baseline and simultaneous sustained public ingestion and retrieval. Offered rates of 1,000, 3,000, and 5,000 searches per minute are measurement points, not service-level objectives.

The existing mixed throughput test uses 48 paginated sessions and 48 updates across four clients with one or two processes. Its 140 unchanged read nodes differ from updated nodes. The test explicitly does not establish absolute service-level performance or production capacity. Its useful machinery must be reused.

The search owner must update and execute the authoritative [search acceptance criteria](2026-09-19-search-acceptance.md). Those criteria retain revision, alias, rebuild, visibility, and point-in-time pagination contracts. Search activation evidence must satisfy that document. Unrelated provider application adoption must not wait for search capacity acceptance.
