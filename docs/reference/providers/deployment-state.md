# Deployment state and provider lifecycle requirements

The declared configuration must account for every required deployment item,
regardless of which provider manages it. Each object and each managed field must
have one writer.

## Deployment coverage

The configuration must declare every field for its deployment area.

Required state:

| Deployment area | Required state |
| --- | --- |
| Guests and hosts | guest identities |
| Guests and hosts | disks |
| Guests and hosts | backup mounts |
| Guests and hosts | addresses |
| Guests and hosts | networks |
| Guests and hosts | resource limits |
| Guests and hosts | startup state |
| Guests and hosts | host overlays |
| Guests and hosts | permissions |
| Guest setup | files |
| Guest setup | downloads |
| Guest setup | artifact installation |
| Guest setup | artifact checksums |
| Guest setup | package versions |
| Guest setup | upgrade restrictions |
| Guest setup | Docker settings |
| Guest setup | forwarding |
| Guest setup | neighbor discovery proxy settings |
| Guest setup | systemd services |
| Guest setup | timers |
| Container services | image digests |
| Container services | engine versions |
| Container services | runtime overlays |
| Container services | placement |
| Container services | replicas |
| Container services | environment values |
| Container services | mounts |
| Container services | ports |
| Container services | readiness checks |
| FoundationDB | cluster identity |
| FoundationDB | members |
| FoundationDB | coordinators |
| FoundationDB | redundancy |
| FoundationDB | backup configuration |
| YugabyteDB | membership |
| YugabyteDB | placement |
| YugabyteDB | runtime settings |
| YugabyteDB | schema version |
| YugabyteDB | roles |
| YugabyteDB | grants |
| YugabyteDB | recovery schedules |
| Search | members |
| Search | models |
| Search | mappings |
| Search | indexes |
| Search | aliases |
| Search | index replacement requirements |
| Audit | signing identities |
| Audit | trusted signers |
| Audit | Kafka topics |
| Audit | retention |
| Audit | consumer configuration |
| Protection and access | backup destinations |
| Protection and access | retention |
| Protection and access | verification schedules |
| Protection and access | restore schedules |
| Protection and access | alarms |
| Protection and access | certificates |
| Protection and access | credential references |
| Protection and access | rotation versions |
| Protection and access | deployment permissions |

## General provider requirements

General providers must satisfy the following lifecycle requirements. A capability
that an existing provider implements does not require a duplicate Tack resource.

Required behavior:

| Area | Required behavior |
| --- | --- |
| Packages | General providers must declare apt versions. |
| Packages | General providers must declare upgrade restrictions. |
| Packages | General providers must perform package removal when the declared lifecycle requires it. |
| Services | General providers must show the required shutdown when removing a managed service. |
| Services | General providers must perform the required shutdown when removing a managed service. |
| Services | General providers must show the required disablement when removing a managed service. |
| Services | General providers must perform the required disablement when removing a managed service. |
| Files | General providers must show readable changes to nonsecret content. |
| Files | General providers must use hashes to detect drift in write-only content. |
| Backup mounts | General providers must compare live mount state. |
| Backup mounts | General providers must manage changes through resources instead of ignored fields and imperative hot-plug steps. |
| Host overlays and permissions | General providers must read installed overlay versions. |
| Host overlays and permissions | General providers must read roles. |
| Host overlays and permissions | General providers must read access rules. |
| Host overlays and permissions | General providers must replace command-triggered installation with observable resources. |
| Host overlays and permissions | General providers must replace command-triggered grants with observable resources. |

## Service requirements

Database layout, replication, and backup behavior must satisfy the
[backup design](../recovery/2026-08-06-backup-rearch-design.md) and its
[acceptance requirements](../recovery/2026-08-08-backup-rearch-acceptance.md).
The data-tier migration's sequence and operator confirmations do not define how
providers adopt existing resources.

Search resources must satisfy the
[search design](../search/2026-09-19-search-design.md) and its
[acceptance requirements](../search/2026-09-19-search-acceptance.md).
General search resources must not independently switch aliases or delete indexes
that Tack's coordinated replacement lifecycle owns. Search activation requires
separate evidence and does not block adoption of unrelated application resources.
