# Tack provider contract

Tack requires declared state for every part of QA and production deployment. A complete `tofu plan` must show the changes needed to satisfy that state.
This contract governs the broader provider migration in
[TACK-567](https://tack.home.goodkind.io/browse/TACK-567); it does not establish that each resource or environment has completed adoption.

## Provider ownership

A provider is the OpenTofu plugin that reads and changes a managed object. The
deployment must reuse existing providers for supported state and extend those
providers when a general capability is missing. New Tack resources must cover
behavior specific to Tack. Examples include:

- Release-related schema changes.
- Audit policy.
- Coordinated search index replacement.

Configs uses [Proxmox container resources](https://github.com/bpg/terraform-provider-proxmox/blob/main/docs/resources/virtual_environment_container.md)
and [pveguest resources](https://github.com/agoodkind/terraform-provider-pveguest/blob/main/README.md)
for infrastructure and guest configuration. The [shared guest module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/base/main.tf)
combines package, file, and service resources. The reuse rule also applies to:

- Containers.
- Databases.
- Kafka.
- Search.
- Object storage.
- Proxies.
- Certificates.
- Schedules.

Configs defines the combined deployment, while Tack supplies application-specific
resources. Those resources use the existing Proxmox guest API when they need
guest execution. This transport does not require a new guest agent or management
service, and a complete plan includes changes from every participating provider.
A Tack provider must not duplicate these resource capabilities when existing providers support them:

- Guest resources.
- Package resources.
- File resources.
- Service resources.

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
Existing owners continue managing each area until its separate migration completes:

- Guest files.
- Runtime secret provisioning.
- Shared infrastructure.

## Deployment coverage

<a id="general-provider-requirements"></a>
<a id="service-requirements"></a>

Declared state and general resource lifecycles must satisfy the
[deployment state and provider lifecycle requirements](providers/deployment-state.md).

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
Refresh and planning must not perform any of these operations:

- Initialize or repair stores.
- Rotate credentials.
- Restart services.
- Otherwise change managed deployment state.

Plans and structured settings must satisfy these requirements:

- A plan must show the before and after values for changed nonsecret settings.
- A plan must show required restarts.
- A plan must show required migrations.
- A plan must show required replacements.
- A plan must show required deletions.
- Structured settings must use typed fields.
- Structured settings must use stable member identifiers.
- A configuration payload must expose its field or text changes. Showing only its checksum does not explain which settings will change.
- Plans must include exact release digests.
- Plans must include pending migrations.
- Plans must include any required search copying.
- Plans must include any required alias switch.
- Plans must include any required index retirement.
- Plans must identify the required order to preserve a working cluster.
- Plans must identify the available members needed to preserve a working cluster.

Command triggers, ignored changes, and
[provisioners](https://opentofu.org/docs/language/resources/provisioners/syntax/)
do not satisfy planning for the objects they change.

Generated values may remain
[unknown before creation](https://opentofu.org/docs/language/expressions/references/#values-not-yet-known),
but plans must show that uncertainty and reject changes when they cannot check a
required condition. Secret values must not appear in:

- State.
- Plans.
- Logs.
- Diagnostics.

Plans explain credential updates through references and rotation versions.

## Apply, removal, and recovery

- Apply must obey OpenTofu's [state locks](https://opentofu.org/docs/language/state/locking/) and existing deployment locks.
- Before making an incompatible change, apply must reject changed conditions that invalidate the saved plan.
- Tack operations must follow the [operator identity and audit contract](audit/operator-identity.md).

Apply must verify the resulting state before reporting completion. After an
interruption, the provider must read completed and unfinished work so another
apply can continue without resetting cluster identity or repeating destructive
initialization. A command's successful exit does not establish the required state.

Resource removal must perform the declared removal behavior. The plan must
identify every affected object and dataset.

- Package removal must follow the affected resource's declared lifecycle.
- Service shutdown must follow the affected resource's declared lifecycle.
- Backup retention must follow the affected resource's declared lifecycle.
- Volume deletion must follow the affected resource's declared lifecycle.

## Adoption and acceptance

- [Import](https://opentofu.org/docs/cli/commands/import/) must read existing objects and compare them with their declarations before a provider becomes their writer.
- Import must preserve cluster identities and data volumes without repeating initial setup.
- Fresh deployments must create absent objects through their resource lifecycles.
- Ansible and service startup must stop writing fields that a provider has adopted.
- Service startup must verify that configuration without changing it.

Every deployment field and setup step requires its own evidence record.

Each record must include:

- Owning provider.
- Live reader.
- Planned changes.
- Apply behavior.
- Removal behavior.
- Acceptance evidence.

Every provider release must satisfy these requirements before its resources replace an existing deployment step.

- Acceptance must use real provider interfaces on disposable systems with the deployed engines.
- Acceptance must cover reused resources.
- Acceptance must cover extended resources.
- Acceptance must cover new resources.

Acceptance requires evidence for each deployment and recovery case.

Acceptance evidence:

| Acceptance case | Required evidence |
| --- | --- |
| Fresh deployment | The declared resources create the complete environment without manual setup commands. |
| Existing deployment | Import compares live state without resetting cluster identities or replacing data volumes. |
| External change | A plan shows changed state, and apply restores the declaration without changing unrelated settings. |
| Interrupted apply | The next apply recognizes completed work and finishes pending work without repeating destructive initialization. |

- Acceptance must verify engine compatibility.
- Acceptance must verify Tack's audit requirements.
- Planning must not change deployed state.
- A completed apply must produce a subsequent plan without changes.
- A second apply must not perform extra work when the declaration already matches the system.

## Current schema resource

Read the [tack_schema resource reference](providers/tack-schema.md) for schema
behavior and audit identity inputs.
