# Operator identity and the ops audit choke-point

Durable rules for how `./server ops` commands identify the operator and record to
the compliance ledger. This file holds settled rules and points to the source of
truth for specifics; it does not restate values that live in code. If this file
and the code disagree, the code wins; fix this file.

Origin: TACK-328 (audit ops commands). It unblocks TACK-327 (lock prod data-plane
access to the ops surface), because locking access to ops is only safe once ops
leave an audit trail.

## The choke-point (binding, do not weaken)

Exactly one place records every CLI ops command: the `RunE` choke-point in
`internal/clispec` (`cobra.go` / `audit.go`). Every command declares a static
`audit.AuditSpec`; the zero value (empty verb) is the only opt-out, used by
`serve`.

Do not weaken or bypass it:

- No command talks to Kafka and no command writes `audit.events`. Events go into
  an outbox (FoundationDB or YugabyteDB), the relay ships them to Kafka, and the
  audit-consumer stays the only ledger writer.
- No mutating or reading command ships without an `AuditSpec` verb.
- Do not remove or reorder the gate sequence: resolve identity, then record, then
  run (or record-with-the-change in one transaction for FDB ops).
- Do not add a per-command flag or env that turns recording off.

The mutation guarantee is that no mutation is ever unrecorded, from day 0. A
command that changes FoundationDB commits its event in the same transaction as
the change, so the two can never disagree. Every other mutation commits an intent
row before it starts and aborts if that write fails. The per-class table and the
bootstrap exception live in the design doc.

## Identity is pluggable (binding, keep it pluggable)

Operator identity comes from an `audit.OperatorIdentitySource` (interface in
`internal/audit`). The choke-point depends only on that interface. Keep it
pluggable:

- Never inline an identity lookup into the choke-point or a command.
- Add identity mechanisms as new source types, selected at the one construction
  site, never by editing the choke-point's logic.

The audit actor kind for an operator is `operator`. The principal's `Source`
field records which mechanism resolved it (`git`, `flag`, `oidc`), so the ledger
shows how each identity was established.

## Identity sources: local git config and flags

One source reads the operator's local git identity (`user.name`, `user.email`)
from the gitconfig file, so the person already configured on the machine is the
recorded operator. The actor id is a stable UUIDv5 derived from the email, so the
same person gets the same id with no registry.

Another source reads `--operator-id`, `--operator-email`, `--operator-name` flags,
used in containers that have no gitconfig. Identity is never an environment
variable and never a hardcoded field in `config.Config`.

Both sources are self-asserted: they trust the local config or the typed flag. The
dry-run gate below is what keeps that safe, by forcing the human to confirm the
identity before any action. OIDC is a verified source the same seam takes when an
IdP is wired; it does not replace these, it joins them.

## Dry-run by default (binding)

Every audited command is dry-run by default. In dry-run it resolves and prints the
operator identity and the action it would take, changes nothing, and records
nothing. The operator reads the printed identity, confirms it is correct, then
re-runs with `--execute` to act.

`--execute` is the single action gate. Without it, no command mutates and no event
is recorded. With it, the command runs and the choke-point records one event.

The point of the gate is that a wrong or shared local identity cannot silently
act: the human must see the identity and opt in each time.

## Where events land

Global ops (migrate, seed-roles, provision, deploy, backup, reindex, backfill)
record on the reserved system org. Entity ops (repair apply) resolve the target
node's real org so the operator action appears on that customer's per-org chain.
The constant and the resolver are named in the design doc.

## Additional identity source: OIDC / SSO / IdP

The flag and git sources are self-asserted. A verified source confirms a signed
token, so prod can require proven identity and forbid the self-asserted sources.
Adding it is one new source type plus IdP config plus a selector; the choke-point,
the commands, the event shape, and `AuditSpec` do not change.

An OIDC source must define:

1. Where the token comes from: an `--operator-token` flag or a short-lived token
   cached by a `tack login` device-authorization flow (RFC 8628). Not an env var.
2. IdP config (infra, like the Kafka broker address): issuer URL, allowed audience
   (client id), JWKS URI discovered from `<issuer>/.well-known/openid-configuration`,
   and an authorization claim (for example, `groups` must contain `tack-operators`).
3. What `Resolve` verifies: JWT signature against cached JWKS (RS256 or ES256),
   `iss`, `aud`, `exp`/`nbf`/`iat` within skew, and the operator authorization
   claim, so authentication alone is not enough.
4. Claim mapping to `OperatorPrincipal`: `ID` is a stable UUIDv5 from `iss` plus
   `sub`; `Email` from a verified `email`; `Name` from `name`; `Source` is `oidc`;
   `Issuer`, `Subject`, and `SessionID` ride along as verified provenance.

Automation (CI deploy) uses a workload identity (client-credentials or
SPIFFE/mTLS) mapped to the `service` actor kind, not a human OIDC token.
Per-command authorization (which operators may run which verbs) is a later layer
on top, not part of the first OIDC step.

## Source of truth

- Design and implementation (one doc, no drift):
  `docs/superpowers/2026-06-08-audit-ops-commands.md`
- Code: choke-point in `internal/clispec`; primitives, `AuditSpec`, `SystemOrgID`,
  and `OperatorIdentitySource` in `internal/audit`; the git and flag sources in
  `internal/cli`.
