# Deployment state and provider lifecycle requirements

The declared configuration must account for every required deployment item,
regardless of which provider manages it. Each object and each managed field must
have one writer.

## Deployment coverage

Each deployment area requires its own declared state.

Required state:

| Deployment area | Required state |
| --- | --- |
| Guests and hosts | The configuration must declare guest identities. |
| Guests and hosts | The configuration must declare disks. |
| Guests and hosts | The configuration must declare backup mounts. |
| Guests and hosts | The configuration must declare addresses. |
| Guests and hosts | The configuration must declare networks. |
| Guests and hosts | The configuration must declare resource limits. |
| Guests and hosts | The configuration must declare startup state. |
| Guests and hosts | The configuration must declare host overlays. |
| Guests and hosts | The configuration must declare permissions. |
| Guest setup | The configuration must declare files. |
| Guest setup | The configuration must declare downloads. |
| Guest setup | The configuration must declare artifact installation. |
| Guest setup | The configuration must declare artifact checksums. |
| Guest setup | The configuration must declare package versions. |
| Guest setup | The configuration must declare upgrade restrictions. |
| Guest setup | The configuration must declare Docker settings. |
| Guest setup | The configuration must declare forwarding. |
| Guest setup | The configuration must declare neighbor discovery proxy settings. |
| Guest setup | The configuration must declare systemd services. |
| Guest setup | The configuration must declare timers. |
| Container services | The configuration must declare image digests. |
| Container services | The configuration must declare engine versions. |
| Container services | The configuration must declare runtime overlays. |
| Container services | The configuration must declare placement. |
| Container services | The configuration must declare replicas. |
| Container services | The configuration must declare environment values. |
| Container services | The configuration must declare mounts. |
| Container services | The configuration must declare ports. |
| Container services | The configuration must declare readiness checks. |
| FoundationDB | The configuration must declare cluster identity. |
| FoundationDB | The configuration must declare members. |
| FoundationDB | The configuration must declare coordinators. |
| FoundationDB | The configuration must declare redundancy. |
| FoundationDB | The configuration must declare backup configuration. |
| YugabyteDB | The configuration must declare membership. |
| YugabyteDB | The configuration must declare placement. |
| YugabyteDB | The configuration must declare runtime settings. |
| YugabyteDB | The configuration must declare schema version. |
| YugabyteDB | The configuration must declare roles. |
| YugabyteDB | The configuration must declare grants. |
| YugabyteDB | The configuration must declare recovery schedules. |
| Search | The configuration must declare members. |
| Search | The configuration must declare models. |
| Search | The configuration must declare mappings. |
| Search | The configuration must declare indexes. |
| Search | The configuration must declare aliases. |
| Search | The configuration must declare index replacement requirements. |
| Audit | The configuration must declare signing identities. |
| Audit | The configuration must declare trusted signers. |
| Audit | The configuration must declare Kafka topics. |
| Audit | The configuration must declare retention. |
| Audit | The configuration must declare consumer configuration. |
| Protection and access | The configuration must declare backup destinations. |
| Protection and access | The configuration must declare retention. |
| Protection and access | The configuration must declare verification schedules. |
| Protection and access | The configuration must declare restore schedules. |
| Protection and access | The configuration must declare alarms. |
| Protection and access | The configuration must declare certificates. |
| Protection and access | The configuration must declare credential references. |
| Protection and access | The configuration must declare rotation versions. |
| Protection and access | The configuration must declare deployment permissions. |

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
