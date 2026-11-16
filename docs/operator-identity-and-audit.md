# Operator identity and audit choke point

Operator commands resolve the person or service requesting an action before
displaying a dry run or executing the command. Installation commands require
identity before creating audit storage.

## Shared audit choke point

Operator commands use one shared audit gate, or choke point, before executing
their work. For ordinary commands, the gate determines which identity to record
and saves the required attempt record before execution. A missing identity or a
failure to save the required record prevents ordinary command execution.

Commands declare their audited action through the shared command registry.
Individual commands do not bypass the gate or disable recording. Commands write
audit events to database outboxes, which store events awaiting delivery, rather
than writing the permanent audit ledger directly.

Identity lookup uses the shared `audit.OperatorIdentitySource` interface. Adding
an identity source does not require each command to implement its own lookup.

## Dry runs

Without `--execute`, an audited command displays the operator identity and
previews its action. A preview that reads system state requires an access
attempt record before reading. A dry run that only prints the proposed action does not
access system state. Both preview and execution require operator identity.

## Asserted identity

Tack accepts operator identity from global Git configuration or command-line
flags. These sources assert an identity without authenticating it with an
identity provider. Displaying an identity during a dry run does not verify it.

Git identity uses the configured name and email address. Explicit operator
identity uses a UUID, email address, and optional display name. Service identity
uses a service name and optional session identifier. Tack prefers service
identity, then explicit operator identity, then Git identity. A session
identifier requires a service name.

Tack rejects explicit operator identities with a missing email address or an
invalid or all-zero UUID.

## Attribution

Operator events record the resolved actor, identity source, operation identifier,
and request correlation. Service events can also identify an accountable human
operator through an operator UUID, email address, and service session identifier.
This attribution does not grant permission to perform an action.

Recorded identity sources distinguish Git, flag, and service assertions.
Verified identity requires authentication evidence beyond these assertions.
Tack does not currently verify operator identity through an OpenID Connect token.
