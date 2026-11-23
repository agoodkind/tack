# Operator identity and audit choke point

An operator is a person or service that runs Tack's administrative commands.
Tack records that identity with each audited command. Identity remains required
during initial installation, before audit storage exists.

## Shared audit choke point

A shared audit choke point is a check every operator command must pass before
running. For ordinary commands, Tack identifies who requested the command and
saves an attempt record. Tack refuses execution if either step fails.

Every command declares its audited action in a shared command registry and uses
a common identity lookup. Commands cannot bypass this check or disable
recording.

## Identity sources

An identity source supplies a name or identifier for whoever requested a
command. Tack reads this information from command options or global Git
configuration. Both sources provide an asserted identity. Authentication
verifies that a caller controls the identity they claim.

Tack chooses the first available source in this order:

| Priority | Source | Identity information |
| --- | --- | --- |
| 1 | Service options | Service name and optional session identifier, which identifies a service run. |
| 2 | Operator options | Operator UUID, email address, and optional display name. A UUID is a standardized unique identifier. |
| 3 | Global Git configuration | Required email address and optional configured name. |

Explicit operator identity requires an email address and a valid UUID that does
not contain only zeros. A session identifier requires a service name.

## Attribution

Attribution records who requested each command or operation and which identity
source Tack used. When a service acts for a person, Tack can record both identities.
That pairing requires the person's operator UUID and email address together
with the service's session identifier. Recording this attribution does not grant
permission to perform an action.

## Dry runs

A dry run previews a command without executing its proposed action.
Without `--execute`, an audited command displays its operator identity and
proposed action. Both preview and execution require identity.

A preview that reads system state requires an access-attempt record before
reading. A preview that only prints its proposed action does not access system
state.
