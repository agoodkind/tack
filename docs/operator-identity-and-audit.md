# Operator identity

Operator commands resolve the person or service requesting an action before
displaying a dry run or executing the command. Installation commands require
identity before creating audit storage.

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
