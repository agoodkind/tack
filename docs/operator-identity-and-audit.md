# Operator identity and audit choke point

An operator is a person or service that runs Tack's administrative commands.
Tack records that identity with each audited command. Identity remains required
during initial installation, before audit storage exists.

## Shared audit choke point

Operator commands use a shared audit gate, also called a choke point. For
ordinary commands, Tack determines the operator identity and saves an attempt
record before executing the action. Missing identity or a recording failure
prevents execution.

Every command declares its audited action through the shared command registry
and uses the same identity lookup. Commands cannot bypass the gate or disable
recording.

## Identity sources

Tack accepts identity from command options or global Git configuration. These
sources supply asserted identities: they state who requested an action without
proving that the person or service controls that identity. Verified identity
requires authentication, which checks that proof.

Tack chooses the first available source in this order:

| Priority | Source | Identity information |
| --- | --- | --- |
| 1 | Service options | Service name and optional session identifier, which identifies a service run. |
| 2 | Operator options | Operator UUID, email address, and optional display name. A UUID is a standardized unique identifier. |
| 3 | Global Git configuration | Required email address and optional configured name. |

Explicit operator identity requires an email address and a valid UUID that does
not contain only zeros. A session identifier requires a service name.

## Attribution

Audit records associate the chosen identity and its source with the command and
request. When a service acts for a person, Tack can record both identities.
That pairing requires the person's operator UUID and email address together
with the service's session identifier. Recording this attribution does not grant
permission to perform an action.

## Dry runs

A dry run previews a command without executing its action. Without `--execute`,
an audited command displays its operator identity and proposed action. Both
preview and execution require identity.

A preview that reads system state requires an access-attempt record before
reading. A preview that only prints the proposed action does not access system
state.
