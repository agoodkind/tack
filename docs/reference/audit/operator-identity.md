# Operator identity and audit recording

Every operator command must use the shared `clispec` audit entry point. Each command must declare an audit operation, including commands that read data and commands that start a service. An empty operation code must fail validation. Commands must not disable recording through a flag, environment variable, or separate execution path.

These requirements remain binding when implementation defects prevent complete recording. The [complete audit recording contract](2026-10-21-audit-completeness-design.md) defines the required evidence for each operation class.

## Identity

`audit.OperatorIdentitySource` resolves the principal before command execution. The command entry point depends on this interface. New identity mechanisms must implement the interface and use the shared source selector. Commands must not implement their own identity lookup.

Human principals use the `operator` actor kind. Service principals use `service`. Each principal records its identity source. Identity must not come from an application environment variable or a hardcoded configuration field.

The selector first rejects a nonempty `--operator-session` without `--operator-service`. It then selects an identity source.

| Source | Selection and identity |
| --- | --- |
| Git configuration | When the service name and operator ID are both empty after trimming whitespace, `GitConfigOperatorSource` reads `user.name` and `user.email` from Git configuration files. It derives a stable UUIDv5 from the lowercase email and the permanent operator namespace. Supplying only `--operator-email` or `--operator-name` still selects this source. |
| Operator flags | Without a service name, `--operator-id` selects the flag source. It requires a nonzero UUID and a nonempty `--operator-email` after trimming whitespace. `--operator-name` supplies the optional display name. |
| Service flags | `--operator-service` selects a stable UUIDv5 from the service name and a separate permanent namespace. `--operator-session` records the agent session. |
| Service acting for a human | A service name plus an operator ID requires a session and the same operator ID and email checks as the flag source. The service remains the actor, and `OnBehalfOf` records the accountable human. |

The flag and Git sources assert identity from local input. A service flag also asserts the service identity. A dry run displays the assertion without authenticating the principal.

The Git source honors `GIT_CONFIG_GLOBAL` as an explicit override. Otherwise it reads the XDG Git configuration, or its home-directory fallback, before the home Git configuration. A later readable file overrides an earlier value. The source parses the files without starting Git. A missing readable configuration or email fails identity resolution.

The Configs [release module](https://github.com/agoodkind/configs/blob/main/opentofu/modules/tack-release/main.tf) supplies the release service, commit session, accountable operator flags, and deployment commit. It derives the human UUIDv5 from the lowercase accountable email using the operator namespace. These declarations establish release command inputs; they do not prove a live release or authenticate an operator.

## Execution and previews

Audited commands default to preview and must resolve identity in both preview and execute modes. A preview must print the resolved identity and proposed action. A preview that only prints a proposed action must not perform that action or record a fictitious execution. `--execute` authorizes the command action.

A human operator checks that the preview shows the correct identity, then reruns the command with `--execute`. The command resolves the identity again for execution without checking whether a preview ran.

A preview that queries system state performs a read. It must save admission and final evidence before printing that state, even without `--execute`.

For typed commands, `Class: audit.OperationRead` selects a handler that captures text and JSON the command writes to its result sink. It prints those results only after it finalizes their audit evidence. The handler does not capture messages the command writes directly to its logger. `audit query` and `ops auth token-list` declare the read class. A command that returns user data must finalize evidence of its actual result and returned identities before releasing that data.

`audit signers` and `audit verify` declare only `Reads: true`. With `--execute`, their handler writes an access event before it runs the command. It does not finalize evidence of the returned identities or output. Both commands still require that evidence under the complete recording contract.

Every permanent command must declare `clispec.Permanent`. A one-time command declares its ticket and literal removal date in `clispec.Lifetime`, registers under `backfill`, and uses the framework's `once-` prefix. The build must fail after the removal date. An old implementation plan must not keep expired one-time commands available.

## Organization and evidence

Global operations use the reserved nonzero system organization. An entity operation must resolve the target's actual organization. The operation must record its complete typed target and actual changes rather than substitute the system organization for an unresolved entity.

FoundationDB product mutations and SQL authentication-row changes must commit their evidence in the transaction that commits the change. External operations must save intent before effects and record observed outcomes and committed steps. Intent and result events use distinct event IDs and share an operation ID. An interrupted or uncertain result must remain distinguishable from success.

Commands must write pending events to the appropriate durable outbox. The relay publishes those events to Kafka. The audit consumer alone appends permanent ledger events. Commands must not write directly to `audit.events` or replace the outbox with direct Kafka publication.

## First-time setup

Only operations that create required audit infrastructure may defer recording while that infrastructure does not exist. Identity remains mandatory. The operation must save its original intent, step evidence, and observed result as soon as the new outbox can accept them. Ordinary runtime commands must refuse unavailable authentication or recording; they must not use the setup exception.

For commands that create audit infrastructure, the current dispatcher can run after the outbox rejects intent because of an authentication failure, even when its probe finds both the outbox table and operator login. That branch violates the first-time setup rule.

`ops provision` checks or initializes FoundationDB, applies migrations, prepares audit roles, starts configured continuous FoundationDB backup, and then seeds product data when both `SEED_EMAIL` and `SEED_NAME` are set. An environment that needs initial data but lacks either setting requires the direct initial seed procedure. A deferred seed represents an incomplete run. Provisioning must run again after the app starts. Production must not run `configure new` or use `--allow-fdb-init`. An unsuccessful status query does not prove an empty cluster.

`ops ledger audit-bootstrap` runs migrations and seeds audit login roles on the first ledger node before joining nodes can perform audited waits. It refuses to run if the migration table, operator outbox, or operator login already exists, or if it cannot inspect the ledger. The setup boundary does not authorize general unrecorded reads or writes.

## Ledger access

Ledger reads and redaction use host commands and dedicated reader or redactor connections. The running app and MCP tools must not receive those connections. The host interface includes `audit query`, `audit get`, `audit export`, and `audit redact-actor`. Audit queue operations use `audit-ops`. The recorded diagnostic `ops db sql` uses `tack-ops` and requires its reason, intent, alarm delivery, and one statement in a read-only session.

The host command `audit query` requires a nonzero `--org` UUID. It limits each page and can resume before the last event time and sequence. The storage filter accepts the zero UUID only with `NilOrg` and rejects `NilOrg` with a nonzero UUID. The host command does not expose that filter option.

The application must connect as `tack_app`, a separate nonsuperuser login with the `app_auth` role. It can use the authentication tables and execute specific audit functions. It cannot update `api_tokens` directly, access the operator outbox or permanent ledger tables directly, or manage the migration ledger. The audit consumer updates token last-use information; `tack_migrator` owns normal migrations. The QA legacy-ledger fixture validates its write target and opens a separate writer connection before it can write a historical-format test row.

Credential inputs must use environment variables or secret-file references. Logs, audit evidence, and error messages must exclude passwords, bearer values, and connection strings containing credentials.

## API token issuance and recovery

`ops auth token-create` matches an active user's email after trimming whitespace and preserving its case. `ops auth token-list` and `ops auth token-revoke` can resolve inactive holders. Issuance and revocation use the holder's sole organization when the holder has exactly one membership. With zero or multiple memberships, they use the reserved system organization.

`token-create` selects a token ID before insertion, stores only the hash of the bearer value, and returns the bearer once in the successful command result. Each executed create or revoke attempt that passes identity and audit validation gets a new operation ID. After saving intent and running the command, the handler attempts to record its result even if the command failed. A failed outbox write returns a recording error. Revocation requires the token ID. If `--id` contains an invalid value, the command rejects it without echoing the input.

The command uses recorded revocation for both recovery cases. Each revocation gets a 30-second deadline independent of request cancellation.

| Failure | Recovery and operator action |
| --- | --- |
| The insert reports an error. | The command tries to revoke the preselected ID. Successful revocation confirms removal. A missing token confirms absence. Any other revocation error leaves the state unknown. Run `ops auth token-list --email <holder-email> --execute`. Without `--execute`, the preview does not list tokens or establish absence. If the ID appears, run `ops auth token-revoke --id <id> --execute`. |
| The command cannot display the bearer after issuance. | The command tries to withdraw the issued token. If withdrawal succeeds, it reports removal. If withdrawal fails, it reports the token ID for manual revocation with `ops auth token-revoke --id <id> --execute`. |

## Additional identity mechanisms

The current selector does not implement OpenID Connect. A future source must use `audit.OperatorIdentitySource` and verify a signed identity token before resolving the operator. Production policy can require that verified source instead of locally asserted identities.

The provider and command must support these inputs:

| Input | Requirement |
| --- | --- |
| Token | Accept `--operator-token` or a short-lived token that `tack login` caches after device authorization under RFC 8628. Do not obtain the token from an environment variable. |
| Provider | Configure the issuer URL and allowed audience (client ID). Discover the signing keys through the issuer's `/.well-known/openid-configuration` document. |
| Authorization | Require a configured claim, such as a `groups` claim that includes `tack-operators`. |

The source must perform these checks and identity mappings:

| Check or field | Requirement |
| --- | --- |
| Signature | Verify an RS256 or ES256 signature against the provider's cached published keys. |
| Claims | Check issuer, audience and operator authorization. Check expiry, not-before time and issued-at time with the permitted clock difference. |
| Principal | Derive the stable UUIDv5 from issuer and subject. Require verified email and use the `name` claim for the display name. |
| Provenance | Record `oidc` as the source and preserve the verified issuer, subject and session. |

Under this design, automation must authenticate with a workload identity and record the `service` actor kind. It uses client credentials or a SPIFFE identity with mutual TLS. A human OpenID Connect token cannot serve as the automation credential. Per-command authorization remains a separate requirement from identity verification.
