# Declarative Tack deployment specification

Every part of Tack deployment in QA and production must have declared state that
OpenTofu can compare with the running system. A complete `tofu plan` must show the
changes needed to make the system match that state, including work currently
performed by deployment commands. This specification defines the replacement
design for TACK-567.

## Provider responsibilities

Use existing providers wherever they support the required state, including the
[Proxmox container
resources](https://github.com/bpg/terraform-provider-proxmox/blob/main/docs/resources/virtual_environment_container.md)
and [pveguest resources](https://github.com/agoodkind/terraform-provider-pveguest/blob/2a4d0e51aab5f43111949bc0609c729013535be2/README.md).
The existing [Configs guest
configuration](https://github.com/agoodkind/configs/blob/1c40b0858ee5c18f44654e416feb90efe794118e/opentofu/guest/base/main.tf)
already uses guest resources for files, packages, and services. Add missing general
capabilities to those providers, and add Tack resources only for behavior specific
to Tack. Apply this rule to every deployment layer, including containers,
databases, Kafka, search, object storage, proxies, certificates, and schedules.
A complete deployment plan must include changes from every provider.

| Layer | Provider responsibility | State covered |
| --- | --- | --- |
| Infrastructure | Existing infrastructure providers manage infrastructure. | Guests, disks, mounts, addresses, networks, resource limits, startup state, host overlays, and host permissions. |
| Guest setup | Existing guest resources manage guest setup. | Files, downloads, artifact checksums and installation, packages, versions, automatic upgrade restrictions, Docker setup, forwarding, neighbor discovery proxy settings, services, and timers. |
| Workloads | Reuse general runtime resources that satisfy the planning contract. | Image digests, engine versions, runtime overlays, placement, replicas, environment values, mounts, ports, and readiness. |
| Service configuration | Reuse general service resources wherever they support the required state. | Database topology and settings, roles and grants, Kafka topics, search engine objects, object storage, proxies, and certificates. |
| Operating policies | Reuse general policy and scheduling resources wherever they support the required state. | Backup destinations, retention, recovery schedules, verification schedules, restore rehearsals, and alarms. |
| Tack behavior | Tack resources manage behavior specific to Tack. | Release-specific schema evolution, audit policy, application compatibility checks, and coordinated search replacement. |

A Docker package declaration uses the existing guest resource. This excerpt omits
the guest creation and other Docker configuration declarations.

```hcl
resource "pveguest_apt_packages" "docker" {
  node     = var.node
  vmid     = var.vmid
  kind     = "lxc"
  packages = var.docker_packages
}
```

Do not add a Tack package resource for this declaration. Reuse the existing
resource interface and extend its general capabilities when the contract requires
it. Apply the same rule to guest, file, service, and other supported resources.

Configs declares the environment and its deployment composition. Tack implements
the resources and application behavior specific to Tack. Existing guest execution
through the Proxmox API provides transport; the design does not require a new guest
agent or management service. Each deployed object and each managed field must have
one writer.

Database state includes FoundationDB cluster identity, members, coordinators,
redundancy, and backup configuration. YugabyteDB state includes membership,
placement, runtime settings, schema version, roles, grants, and recovery schedules.
Search state includes members, models, mappings, indexes, aliases, and conditions
that require index replacement.

Audit state includes signing identities, trusted signers, Kafka topics, retention,
and consumer configuration. Operating policies include backup destinations,
retention, scheduled verification, and restore rehearsals. Access configuration
includes certificates, credential references, rotation versions, and deployment
permissions. Secret values must remain absent from state, plans, logs, and
diagnostics. Plans show credential references and rotation versions.

## General provider gaps

The existing providers must satisfy the complete planning contract before adoption.
Their current resource definitions establish the following gaps.

| Required capability | General provider change |
| --- | --- |
| Package state | Add declared apt versions and automatic upgrade restrictions. Define package removal. Current apt and local Debian-package destruction only removes their OpenTofu records. |
| Service removal | Define shutdown and disablement when the declared lifecycle requires them. Current systemd resource destruction only removes its OpenTofu record. |
| Readable file drift | Read changed nonsecret content for a field or text diff. Preserve hash-based drift detection for write-only content. |
| Backup mounts | Manage mounts independently and compare their live state. Replace ignored mount changes and imperative hot-plug steps with planned resource changes. |
| Host overlays and permissions | Read installed overlay versions, roles, and access rules. Replace command-triggered installation and permission grants with observable resources. |

Extend general providers for these capabilities. Deployment acceptance must verify
engine compatibility and Tack's audit requirements.

## Declared state and live reads

A resource must represent an observable object or policy under OpenTofu's
[resource lifecycle](https://opentofu.org/docs/language/resources/behavior/) rather
than an instruction to run a command. For example, a schema resource declares a
required schema version, and an index resource declares a required index
configuration. Apply performs the migration or replacement needed to satisfy that
declaration.

Refresh reads the current state of every managed object through authenticated
interfaces. The provider must distinguish an object that is absent from an object
that it cannot read because a service is unavailable or access failed. An
unavailable object produces an error without removing the recorded resource.
Refresh and planning must not initialize, repair, rotate credentials, restart
services, or otherwise change managed deployment state.

[Import](https://opentofu.org/docs/cli/commands/import/) must read existing resources
without resetting cluster identity, replacing data volumes, or repeating first-time
initialization. Differences between declared and observed state must appear in the
next plan, including changes made outside OpenTofu.

## Complete plans

OpenTofu's [plan model](https://opentofu.org/docs/cli/commands/plan/) compares live
objects with configuration before proposing changes. Tack deployment plans must
show changed values and required restarts, migrations, replacements, and deletions.
Represent structured settings with typed attributes and stable member keys. Show
the changed fields of nonsecret configuration. An opaque payload or checksum diff
does not explain those changes. Plans must include immutable release digests,
pending migrations, and any required search copying, alias switch, or retirement.
Plans must identify the required order and any quorum condition, which is the
minimum number of available members needed for a cluster to operate. A configuration
change must not hide these effects inside an arbitrary command. OpenTofu cannot
model the object changed by a
[provisioner](https://opentofu.org/docs/language/resources/provisioners/syntax/).

Generated values may remain [unknown before
creation](https://opentofu.org/docs/language/expressions/references/#values-not-yet-known),
but the plan must display that uncertainty. Planning must fail when an unknown
value prevents checking a required condition. An ignored mount change or a command
trigger does not satisfy complete planning for the object that the command changes.

## Apply and recovery

Apply must reject changed conditions that invalidate a saved plan before performing
an incompatible change. Use OpenTofu's [state
locking](https://opentofu.org/docs/language/state/locking/) to prevent concurrent
writes to deployment state.
Operations must also obey existing deployment locks, and Tack changes must use the
existing [operator identity and audit
contract](../../operator-identity-and-audit.md).

Apply must verify the resulting state before reporting completion. After an
interruption, the provider must read completed and unfinished work so another apply
can continue without repeating destructive initialization. A successful command
alone does not prove that the required state exists.

Removing a resource must perform its defined removal rather than only deleting its
OpenTofu record. The plan must expose the objects and data affected by removal.
Package removal, service shutdown, backup retention, and data-volume deletion must
follow the declared lifecycle of the affected resource.

## Existing service contracts

Database layout, replication, and backup behavior must satisfy the [backup
design](2026-08-06-backup-rearch-design.md). Backup verification must satisfy the
applicable [backup acceptance requirements](2026-08-08-backup-rearch-acceptance.md).
The original data-tier migration sequence does not define the provider adoption
sequence.

Search behavior must satisfy the [search design](2026-09-19-search-design.md), and
search changes must satisfy the applicable [search acceptance
requirements](2026-09-19-search-acceptance.md). Search activation requires its own
evidence and does not block adoption of unrelated application resources.

## Adoption and acceptance

Import each existing managed object and compare it with the declared configuration
before transferring its fields to a provider. Fresh deployments create absent
objects through their resource lifecycles. Transfer the affected fields from the
previous deployment tool before allowing provider changes, so Ansible cannot
recreate or overwrite a provider-managed object. Replace imperative deployment
steps with resources as their responsibilities transfer. Service startup must verify
provider-managed configuration instead of changing that configuration outside an
apply.

Account for every deployment field and imperative setup step in the acceptance
inventory. For each managed object, record its owning provider, live reader,
planned changes, apply and removal behavior, and acceptance evidence. Reused,
extended, and new resources must satisfy the same planning requirements.

Acceptance must use disposable systems with the deployed engines and provider
interfaces. Evidence must cover a fresh deployment, import of an existing
deployment, detection and repair of external changes, and recovery after an
interrupted apply. Planning must change no deployed state, and a completed apply
must be followed by a plan with no changes. A second apply must perform no extra
work when the declared state already matches the system.

The installed provider releases must meet this contract before their resources
replace existing deployment responsibilities. Publishing this specification does
not establish implementation or deployment acceptance.
