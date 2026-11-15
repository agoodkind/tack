# Operator identity and the ops audit choke-point

TACK-328 requires ops commands to record an audit trail before TACK-327 restricts
production data-plane access to those commands.

## The choke-point (binding, do not weaken)

The shared `RunE` handler in `internal/clispec` (`cobra.go` / `audit.go`) records
every CLI ops command. Every command, including `serve`, declares a static audit spec
with a verb. An empty verb fails the gate and the coverage test fails the build.
Commands cannot opt out.

Do not weaken or bypass it:

- Commands must write events to a FoundationDB or YugabyteDB outbox, not Kafka or
  `audit.events`. The relay publishes outbox events to Kafka. The audit-consumer
  remains the only ledger writer.
- Every mutating or reading command must declare an `AuditSpec` verb.
- Preserve the gate sequence: resolve identity, record, then run. FDB ops record
  the event and change in one transaction.
- Do not add a per-command flag or environment variable that disables recording.

The command records the operator, action, target, time, and request correlation
before execution. External commands also record the attempted action for each
step. Tack records access denials separately. FoundationDB mutations
record the actual changes in the transaction that commits them.

## Identity is pluggable (binding, keep it pluggable)

The choke-point resolves operator identity through `audit.OperatorIdentitySource`,
an interface in `internal/audit`. Keep it pluggable:

- Never inline an identity lookup into the choke-point or a command.
- Add identity mechanisms as new source types, selected at the one construction
  site, never by editing the choke-point's logic.

The audit actor kind for an operator is `operator`. The principal's `Source`
field records which mechanism resolved it (`git`, `flag`, `oidc`).

## Identity sources: local git config and flags

One source records the operator's local git identity (`user.name`, `user.email`)
from the gitconfig file. It derives a stable UUIDv5 actor id from the email
without a registry.

Another source reads `--operator-id`, `--operator-email`, and `--operator-name`
flags for containers without gitconfig. Identity must not use an environment
variable or a hardcoded field in `config.Config`.

Both sources are self-asserted: they trust the local config or the typed flag.
The dry-run gate requires the operator to confirm the identity before acting.
An additional OIDC source can verify identity through an identity provider (IdP)
using the same interface, alongside the git and flag sources.

## Dry-run by default (binding)

Every audited command defaults to a dry run. The command prints the operator
identity and the action it would take. A preview that reads system state
records the access attempt before reading. A dry run that only prints the
proposed action does not access system state.

Pass `--execute` to perform the requested action.

## Event organization

Global ops (migrate, seed-roles, provision, deploy, backup, reindex, backfill)
record on the reserved system org. Entity ops (repair apply) resolve the target
node's real org and record the operator action on that customer's per-org chain.
The design doc specifies the constant and resolver.

## Additional identity source: OIDC / SSO / IdP

A verified source confirms a signed token. Production can require this verified
identity and forbid self-asserted sources. Implement it with a new source type,
IdP config, and a selector without changing the choke-point, commands, event
shape, or `AuditSpec`.

An OIDC source must define:

1. The token source: an `--operator-token` flag or a short-lived token cached by
   a `tack login` device-authorization flow (RFC 8628), not an environment variable.
2. IdP config (infrastructure, like the Kafka broker address): issuer URL, allowed
   audience (client id), JWKS URI discovered from
   `<issuer>/.well-known/openid-configuration`, and an authorization claim
   (for example, `groups` must contain `tack-operators`).
3. `Resolve` verification: JWT signature against cached JWKS (RS256 or ES256),
   `iss`, `aud`, `exp`/`nbf`/`iat` within skew, and the operator authorization
   claim. Authentication alone is insufficient.
4. Claim mapping to `OperatorPrincipal`: `ID` is a stable UUIDv5 from `iss` plus
   `sub`; `Email` from a verified `email`; `Name` from `name`; `Source` is `oidc`.
   Store `Issuer`, `Subject`, and `SessionID` as verified provenance.

Automation (CI deploy) uses a workload identity (client-credentials or
SPIFFE/mTLS) mapped to the `service` actor kind, not a human OIDC token.
Per-command authorization determines which operators may run which verbs and
requires a later implementation beyond the first OIDC step.

## Implement the audit contract

Follow the [operator audit design and implementation plan](superpowers/2026-06-08-audit-ops-commands.md)
for operation classes, bootstrap behavior, and implementation requirements.
