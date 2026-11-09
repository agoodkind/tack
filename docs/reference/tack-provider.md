# Tack provider contract

Tack requires declared state for every part of QA and production deployment. A complete `tofu plan` must show the changes needed to satisfy that state.
This contract governs the broader provider migration in
[TACK-567](https://tack.home.goodkind.io/browse/TACK-567); it does not establish that each resource or environment has completed adoption.

## Provider ownership

A provider is the OpenTofu plugin that reads and changes a managed object. The
deployment must reuse existing providers for supported state and extend those
providers when a general capability is missing. New Tack resources must cover
behavior specific to Tack, such as release-related schema changes, audit policy,
and coordinated search index replacement.

Configs uses [Proxmox container resources](https://github.com/bpg/terraform-provider-proxmox/blob/main/docs/resources/virtual_environment_container.md)
and [pveguest resources](https://github.com/agoodkind/terraform-provider-pveguest/blob/main/README.md)
for infrastructure and guest configuration. The [shared guest module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/base/main.tf)
combines package, file, and service resources. The reuse rule also applies to
containers, databases, Kafka, search, object storage, proxies, certificates, and
schedules.

Configs defines the combined deployment, while Tack supplies application-specific
resources. Those resources use the existing Proxmox guest API when they need
guest execution. This transport does not require a new guest agent or management
service, and a complete plan includes changes from every participating provider.
A Tack provider must not duplicate guest, package, file, or service resources for
capabilities that existing providers support.

Docker packages use the existing guest resource. This excerpt omits guest creation
and the remaining Docker configuration:

```hcl
resource "pveguest_apt_packages" "docker" {
  node     = var.node
  vmid     = var.vmid
  kind     = "lxc"
  packages = var.docker_packages
}
```

The [shared provider contract](https://github.com/agoodkind/configs/blob/main/docs/superpowers/gueststate/spec.md#12-tack-and-mwan-migration) requires Tack and MWAN to reuse general capabilities.
Each resource group requires its own dependency and acceptance evidence before adoption. Implementing a provider does not establish that an application root declares, imports, or deploys its resources.

QA verification must precede production adoption under the applicable production authorization.
Existing owners continue managing guest files, runtime secret provisioning, and shared infrastructure until their separate migrations complete.

## Deployment coverage

The declared configuration must account for every item below, regardless of which
provider manages it. Each object and each managed field must have one writer.

Required state:

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

## General provider requirements

General providers must satisfy the following lifecycle requirements. A capability that an existing provider implements does not require a duplicate Tack resource.

Required behavior:

| Area | Required behavior |
| --- | --- |
| Packages | General providers must declare apt versions and upgrade restrictions, and perform package removal when the declared lifecycle requires it. |
| Services | General providers must show and perform the required shutdown and disablement when removing a managed service. |
| Files | General providers must show readable changes to nonsecret content, and use hashes to detect drift in write-only content. |
| Backup mounts | General providers must compare live mount state and manage changes through resources instead of ignored fields and imperative hot-plug steps. |
| Host overlays and permissions | General providers must read installed overlay versions, roles, and access rules, and replace command-triggered installation and grants with observable resources. |

## Refresh and planning

A resource declares an object or policy under OpenTofu's
[resource lifecycle](https://opentofu.org/docs/language/resources/behavior/), such
as a required schema version or search index configuration. During
[planning](https://opentofu.org/docs/cli/commands/plan/), the provider reads that
object and identifies the changes needed to satisfy the declaration. A command
invocation does not substitute for the object's state.

Refresh must use authenticated interfaces and distinguish an absent object from
an object that a service failure or permission refusal prevents it from reading.
An unreadable object must produce an error without removing its recorded resource.
Refresh and planning must not initialize or repair stores, rotate credentials,
restart services, or otherwise change managed deployment state.

A plan must show the before and after values for changed nonsecret settings,
along with required restarts, migrations, replacements, and deletions. Structured
settings must use typed fields and stable member identifiers. A configuration
payload must expose its field or text changes; showing only its checksum does not
explain which settings will change.

Plans must include exact release digests, pending migrations, and any required
search copying, alias switch, or index retirement. They must identify the required
order and the available members needed to preserve a working cluster. Command
triggers, ignored changes, and
[provisioners](https://opentofu.org/docs/language/resources/provisioners/syntax/)
do not satisfy planning for the objects they change.

Generated values may remain
[unknown before creation](https://opentofu.org/docs/language/expressions/references/#values-not-yet-known),
but plans must show that uncertainty and reject changes when they cannot check a
required condition. State, plans, logs, and diagnostics must not contain secret
values. Plans explain credential updates through references and rotation versions.

## Apply, removal, and recovery

Apply must obey OpenTofu's [state locks](https://opentofu.org/docs/language/state/locking/)
and existing deployment locks. Before making an incompatible change, apply must
reject changed conditions that invalidate the saved plan. Tack operations must
follow the [operator identity and audit contract](audit/operator-identity.md).

Apply must verify the resulting state before reporting completion. After an
interruption, the provider must read completed and unfinished work so another
apply can continue without resetting cluster identity or repeating destructive
initialization. A command's successful exit does not establish the required state.

Resource removal must perform the declared removal behavior, and the plan must
identify every affected object and dataset. Package removal, service shutdown,
backup retention, and volume deletion must follow the affected resource's
declared lifecycle.

## Service requirements

Database layout, replication, and backup behavior must satisfy the
[backup design](recovery/2026-08-06-backup-rearch-design.md) and its
[acceptance requirements](recovery/2026-08-08-backup-rearch-acceptance.md).
The data-tier migration's sequence and operator confirmations do not define how
providers adopt existing resources.

Search resources must satisfy the
[search design](search/2026-09-19-search-design.md) and its
[acceptance requirements](search/2026-09-19-search-acceptance.md).
General search resources must not independently switch aliases or delete indexes
that Tack's coordinated replacement lifecycle owns. Search activation requires
separate evidence and does not block adoption of unrelated application resources.

## Adoption and acceptance

[Import](https://opentofu.org/docs/cli/commands/import/) must read existing objects
and compare them with their declarations before a provider becomes their writer.
Import must preserve cluster identities and data volumes without repeating initial
setup. Fresh deployments create absent objects through their resource lifecycles.
Ansible and service startup must stop writing fields that a provider has adopted.
Service startup must verify that configuration without changing it.

Every deployment field and setup step requires a record of the owning provider,
live reader, planned changes, apply and removal behavior, and acceptance evidence.
Every provider release must satisfy these requirements before its resources
replace an existing deployment step. Acceptance must use real provider interfaces on disposable systems with the deployed engines and cover reused, extended, and
new resources.

Acceptance requires evidence for each deployment and recovery case.

Acceptance evidence:

| Acceptance case | Required evidence |
| --- | --- |
| Fresh deployment | The declared resources create the complete environment without manual setup commands. |
| Existing deployment | Import compares live state without resetting cluster identities or replacing data volumes. |
| External change | A plan shows changed state, and apply restores the declaration without changing unrelated settings. |
| Interrupted apply | The next apply recognizes completed work and finishes pending work without repeating destructive initialization. |

Acceptance must also verify engine compatibility and Tack's audit requirements.
Planning must not change deployed state. A completed apply must produce a
subsequent plan without changes. A second apply must not perform extra work when
the declaration already matches the system.

## Current schema resource

The Tack provider currently registers `tack_schema` for the YugabyteDB migration
version. It uses the pveguest transport to run `migrate --output json` in the
guest's `tack-ops` Compose service. Status reads omit `--execute` and use `--pull never`.
The resource does not provision the guest or the complete stack.

The resource identifies an installation as `<node>/<vmid>:<install_dir>`.
Import records `node`, `vmid`, and `install_dir`; refresh reads the live migration
version. The provider reports unreadable status as an error and preserves state.
If the Goose migration table does not exist, the provider removes the resource
from state. Delete only removes the resource from state and does not revert migrations.

Planning rejects a target below the applied version or above the image's highest
available migration. A target at or below version 19 requires an applied version
of at least 19. A changed target makes `pending` unknown until apply and lists
the required migrations in a warning. Apply rejects a changed image commit, runs
`migrate --execute --to` with `--expect-from`, and verifies the final version and
pending migrations through that target. A nonzero `--expect-from` requires the matching starting
version; zero does not impose that check.

The provider requires an operator email and derives its version 5 UUID from the
trimmed, lowercased email. Configs uses the same namespace and lowercased email in its
[release module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/tack-release/main.tf).
An agent identity requires both `service` and `session`. A person identity uses `name`. Every Tack command receives the corresponding audit identity flags.
