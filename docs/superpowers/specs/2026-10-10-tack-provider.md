# Declarative Tack deployment specification

Every part of Tack deployment in QA and production must have declared state that
OpenTofu can compare with the running system. A complete `tofu plan` must show the
changes needed to make the system match that state, including work currently
performed by deployment commands. This specification defines the replacement
design for TACK-567.

## Provider responsibilities

Use the existing Proxmox and `pveguest` providers wherever they support the required
state. Add missing general capabilities to those providers, and add Tack resources
only for behavior specific to Tack. A single deployment can use several providers
without reducing the scope of its plan.

| Layer | Provider responsibility | State covered |
| --- | --- | --- |
| Infrastructure | Existing infrastructure providers manage infrastructure. | Guests, disks, mounts, addresses, networks, resource limits, startup state, host overlays, and host permissions. |
| Guest setup | Existing guest resources manage guest setup. | Files, downloads, artifact installation, packages, versions, automatic upgrade restrictions, Docker setup, forwarding, neighbor discovery proxy settings, services, and timers. |
| Application | Tack resources manage application behavior and its dependencies. | Release image digests, engine versions, Tack runtime overlays, artifact checksums, workload placement, replicas, environment values, mounts, ports, and readiness. |
| Data and operations | Tack resources manage service configuration and operating policies. | Database and search state, audit configuration, backups, verification schedules, restore rehearsals, and alarms. |

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
permissions. Secret values must remain absent from plans, logs, and diagnostics.

## Declared state and live reads

A resource must represent an observable object or policy rather than an instruction
to run a command. For example, a schema resource declares a required schema version,
and an index resource declares a required index configuration. Apply performs the
migration or replacement needed to satisfy that declaration.

Refresh reads the current state of every managed object through authenticated
interfaces. The provider must distinguish an object that is absent from an object
that it cannot read because a service is unavailable or access failed. An
unavailable object produces an error without removing the recorded resource.
Refresh and planning must not change the deployed system.

Import must read existing resources without resetting cluster identity, replacing
data volumes, or repeating first-time initialization. Differences between declared
and observed state must appear in the next plan, including changes made outside
OpenTofu.

## Complete plans

Plans must show changed values and the restarts, migrations, replacements, and
deletions needed to apply them. Plans must identify the required order and any
quorum condition, which is the minimum number of available members needed for a
cluster to operate. A configuration change must not hide these effects inside an
arbitrary command or a provisioner.

Generated values may remain unknown before creation, but the plan must display
that uncertainty. Planning must fail when an unknown value prevents checking a
required condition. An ignored mount change or a command trigger does not satisfy
complete planning for the object that the command changes.

## Apply and recovery

Apply must reject changed conditions that invalidate a saved plan before performing
an incompatible change. Operations must obey existing deployment locks, and Tack
changes must use the existing [operator identity and audit
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

Each resource must be imported and compared with the declared configuration before
the provider becomes its writer. Transfer the affected fields from the previous
deployment tool before allowing provider changes, so Ansible cannot recreate or
overwrite a provider-managed object. Replace imperative deployment steps with
resources as their responsibilities transfer.

Acceptance must use disposable systems with the deployed engines and provider
interfaces. Evidence must cover a fresh deployment, import of an existing
deployment, detection and repair of external changes, and recovery after an
interrupted apply. Planning must change no deployed state, and a completed apply
must be followed by a plan with no changes. A second apply must perform no extra
work when the declared state already matches the system.

The installed provider releases must meet this contract before their resources
replace existing deployment responsibilities. Publishing this specification does
not establish implementation or deployment acceptance.
