# Operator identity

Tack resolves the person or service running an operator command through
`audit.OperatorIdentitySource`. The shared command gate resolves identity before
printing a dry run or executing a command. Installation commands resolve identity
before creating audit storage.

## Asserted identity

The current identity sources trust local configuration or command-line flags.
They do not verify the identity with an identity provider. A dry run displays the
resolved identity and proposed action, but that display does not verify either
a person or a service.

The Git source reads `user.name` and `user.email` from global Git configuration
without invoking Git. It derives a stable UUIDv5 from the lowercase email
address. The flag source reads the supplied operator UUID, email, and optional
display name. It rejects a missing email, an invalid UUID, or an all-zero UUID.
Both sources record the actor kind as `operator`.

The service source derives a stable UUIDv5 from the supplied service name and
records the actor kind as `service`. It records a session identifier when the
caller supplies one. The identity selector prefers a service name, then an
operator UUID, then global Git configuration. A session identifier without a
service name causes an error.

Identity comes from these sources rather than an environment variable or a field
in server configuration. The command gate depends on the identity interface;
source selection remains separate from command execution.

## Attribution

Operator events include the resolved actor, identity source, operation identifier,
and request correlation. A service event can also identify an accountable human
operator. That pairing requires the operator UUID, email, and service session
identifier. It does not establish an authorization grant.

The recorded source distinguishes Git, flag, and service assertions. A verified
identity would require authentication evidence beyond those assertions. Tack
does not currently implement an operator identity source that verifies an OpenID
Connect token.
