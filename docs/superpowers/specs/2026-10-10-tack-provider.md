# Declarative Tack deployment specification

Tack deployment currently combines OpenTofu guest creation with Ansible setup and
application commands. Those commands perform changes that OpenTofu cannot compare
with the running system. This specification for TACK-567 requires declared state
for every part of QA and production deployment, with a complete `tofu plan` that
shows the changes needed to satisfy that state.

## Reuse existing providers

A provider is the OpenTofu plugin that reads and changes a managed object. The
deployment must reuse existing providers for supported state and extend those
providers when a general capability is missing. New Tack resources must cover
behavior specific to Tack, such as release-related schema changes, audit policy,
and coordinated search index replacement.

Configs already uses [Proxmox container resources](https://github.com/bpg/terraform-provider-proxmox/blob/main/docs/resources/virtual_environment_container.md)
and [pveguest resources](https://github.com/agoodkind/terraform-provider-pveguest/blob/2a4d0e51aab5f43111949bc0609c729013535be2/README.md)
for infrastructure and guest configuration. The [existing guest declarations](https://github.com/agoodkind/configs/blob/1c40b0858ee5c18f44654e416feb90efe794118e/opentofu/guest/base/main.tf)
combine package, file, and service resources. The same reuse rule applies to
containers, databases, Kafka, search, object storage, proxies, certificates, and
schedules.

Configs defines the combined deployment, while Tack supplies application-specific
resources. Those resources use the existing Proxmox guest API when guest execution
is needed. This transport does not require a new guest agent or management
service, and a complete plan includes changes from every participating provider.

For example, Docker packages use the existing guest resource. This excerpt omits
guest creation and the remaining Docker configuration.

```hcl
resource "pveguest_apt_packages" "docker" {
  node     = var.node
  vmid     = var.vmid
  kind     = "lxc"
  packages = var.docker_packages
}
```

A Tack provider must not introduce duplicate guest, package, file, or service
resources for capabilities that existing providers support.

## Cover the whole deployment

The declared configuration must account for every item below, regardless of which
provider manages it. Each object and each managed field must have one writer.

| Deployment part | Required state |
| --- | --- |
| Guests and hosts | Guest identities, disks, backup mounts, addresses, networks, resource limits, startup state, host overlays, and permissions. |
| Guest setup | Files, downloads, artifact installation and checksums, package versions, upgrade restrictions, Docker settings, forwarding, neighbor discovery proxy settings, systemd services, and timers. |
| Container services | Image digests, engine versions, runtime overlays, placement, replicas, environment values, mounts, ports, and readiness checks. |
| FoundationDB | Cluster identity, members, coordinators, redundancy, and backup configuration. |
| YugabyteDB | Membership, placement, runtime settings, schema version, roles, grants, and recovery schedules. |
| Search | Members, models, mappings, indexes, aliases, and index replacement requirements. |
| Audit | Signing identities, trusted signers, Kafka topics, retention, and consumer configuration. |
| Protection and access | Backup destinations, retention, verification and restore schedules, alarms, certificates, credential references, rotation versions, and deployment permissions. |

## Extend general provider capabilities

The current guest package resources do not fully declare package versions and
upgrade restrictions. Package and systemd resource destruction removes their
OpenTofu records without changing the guest. The existing providers need the
following changes before they can satisfy the deployment requirements.

| Area | Required change |
| --- | --- |
| Packages | Declare apt versions and upgrade restrictions, and perform package removal when the declared lifecycle requires it. |
| Services | Show and perform the required shutdown and disablement when removing a managed service. |
| Files | Show readable changes to nonsecret content, and use hashes to detect drift in write-only content. |
| Backup mounts | Compare live mount state and manage changes through resources instead of ignored fields and imperative hot-plug steps. |
| Host overlays and permissions | Read installed overlay versions, roles, and access rules, and replace command-triggered installation and grants with observable resources. |

## Read and plan changes

A resource declares an object or policy under OpenTofu's [resource lifecycle](https://opentofu.org/docs/language/resources/behavior/),
such as a required schema version or search index configuration. During [planning](https://opentofu.org/docs/cli/commands/plan/),
the provider reads that object and identifies the changes needed to satisfy the
declaration. A command invocation is not a substitute for the object's state.

Refresh must use authenticated interfaces and distinguish an absent object from
an object that cannot be read because a service or permission check failed. An
unreadable object must produce an error without removing its recorded resource.
Refresh and planning must not initialize or repair stores, rotate credentials,
restart services, or otherwise change managed deployment state.

A plan must show the before and after values for changed nonsecret settings, along
with required restarts, migrations, replacements, and deletions. Structured
settings must use typed fields and stable member identifiers. A configuration
payload must expose its field or text changes; showing only its checksum does not
explain which settings will change.

Plans must include exact release digests, pending migrations, and any required
search copying, alias switch, or index retirement. They must identify the required
order and the available members needed to preserve a working cluster. Command
triggers, ignored changes, and [provisioners](https://opentofu.org/docs/language/resources/provisioners/syntax/)
do not satisfy planning for the objects they change.

Generated values may remain [unknown before creation](https://opentofu.org/docs/language/expressions/references/#values-not-yet-known),
but plans must show that uncertainty and reject changes when a required condition
cannot be checked. Secret values must remain absent from state, plans, logs, and
diagnostics; plans explain credential updates through references and rotation
versions.

## Apply changes and recover from interruption

Apply must obey OpenTofu's [state locks](https://opentofu.org/docs/language/state/locking/)
and existing deployment locks. Before making an incompatible change, apply must
reject changed conditions that invalidate the saved plan. Tack operations must
also follow the existing [operator identity and audit contract](../../operator-identity-and-audit.md).

Apply must verify the resulting state before reporting completion. After an
interruption, the provider must read completed and unfinished work so another
apply can continue without resetting cluster identity or repeating destructive
initialization. A command's successful exit does not establish that the required
state exists.

Resource removal must perform the declared removal behavior, and the plan must
identify every affected object and dataset. Package removal, service shutdown,
backup retention, and volume deletion must follow the affected resource's
declared lifecycle.

## Preserve service requirements

Database layout, replication, and backup behavior must satisfy the [backup design](2026-08-06-backup-rearch-design.md)
and its [acceptance requirements](2026-08-08-backup-rearch-acceptance.md). The
original data-tier migration has its own sequence and operator confirmations;
that sequence does not define how providers adopt existing resources.

Search resources must satisfy the [search design](2026-09-19-search-design.md)
and its [acceptance requirements](2026-09-19-search-acceptance.md). General search
resources must not independently switch aliases or delete indexes owned by
Tack's coordinated replacement lifecycle. Search activation requires separate
evidence and does not block adoption of unrelated application resources.

## Adopt resources and prove the result

Use [import](https://opentofu.org/docs/cli/commands/import/) to read existing
objects and compare them with their declarations before a provider becomes their
writer. Import must preserve cluster identities and data volumes without
repeating initial setup. Fresh deployments create absent objects through their
resource lifecycles. Ansible and service startup must stop writing fields that a
provider has adopted, and service startup must verify that configuration without
changing it.

For every deployment field and setup step, record the owning provider, live
reader, planned changes, apply and removal behavior, and acceptance evidence.
Every provider release must satisfy these requirements before its resources
replace an existing deployment step. Acceptance must use real provider interfaces
on disposable systems with the deployed engines and cover reused, extended, and
new resources.

| Acceptance case | Required evidence |
| --- | --- |
| Fresh deployment | The declared resources create the complete environment without manual setup commands. |
| Existing deployment | Import compares live state without resetting cluster identities or replacing data volumes. |
| External change | A plan shows changed state, and apply restores the declaration without changing unrelated settings. |
| Interrupted apply | The next apply recognizes completed work and finishes pending work without repeating destructive initialization. |

Acceptance must also verify engine compatibility and Tack's audit requirements.
Planning must change no deployed state, and a completed apply must produce a
subsequent plan with no changes. A second apply must perform no extra work when
the declaration already matches the system.
