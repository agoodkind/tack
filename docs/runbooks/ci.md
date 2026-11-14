# Update PR CI requirements

PR CI runs static checks, unit tests, binary builds, database patch checks, and
native candidate image builds. Unit tests use `-short` to skip service fixtures.

## Migrate required check names

During an authorized CI transition, retain the app requirements while removing
obsolete Actions requirements. After the new checks pass, require their replacement
contexts before merging the layout change. Preserve application identities,
pull-request rules, signature protection, and ruleset enforcement.

| Existing requirement | Replacement |
| --- | --- |
| `go / Checks` | `Checks / Server` and `Checks / Tack provider` |
| `go / Build` | `Build / Server` and `Build / Tack provider` |
| `go / Release (dry run)` | Remove this empty requirement. |
| `integration tests (FDB)` | Remove this integration requirement. |
| `overlay-drift` | `Checks / Database patches` |
| `build (tack-server, Dockerfile, FDB_VERSION=7.4.6)` | `Images / Publish Server` |
| `build (tack-audit-consumer, Dockerfile.audit-consumer)` | `Images / Publish Audit consumer` |
| `search integration tests (OpenSearch features)` | Remove this integration requirement. |
| `runtime audit coverage` | Remove this integration requirement. |

Keep `GitGuardian Security Checks` and `CodeRabbit` under their existing names.
The GitHub Apps provide those contexts independently of the Actions workflows.
Require all replacement contexts to report success for the pull request's current
head before merging.

## Track QA integration

PR CI does not run database, runtime audit, search, or PostgreSQL provider
integration suites. Their test code and manual Make targets remain available.
QA deployment and acceptance integration remain separate work under
[TACK-602](https://tack.home.goodkind.io/browse/TACK-602).
