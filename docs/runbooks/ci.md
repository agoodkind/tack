# Update CI requirements

CI uses separate checks for server and provider quality, database and search
regressions, audit coverage, database patches, and published QA images.

## Migrate required check names

During an authorized CI transition, retain the app requirements while removing
obsolete Actions requirements. After the new checks pass, require their replacement
contexts before merging the layout change. Preserve application identities,
pull-request rules, signature protection, and ruleset enforcement.

| Existing requirement | Replacement |
| --- | --- |
| `go / Checks` | `Checks / Server` |
| `go / Build` | `Build / Server` |
| `go / Release (dry run)` | Remove this empty requirement. |
| `integration tests (FDB)` | `Tests / Database` |
| `overlay-drift` | `Checks / Database patches` |
| `build (tack-server, Dockerfile, FDB_VERSION=7.4.6)` | `Images / Publish Server` |
| `build (tack-audit-consumer, Dockerfile.audit-consumer)` | `Images / Publish Audit consumer` |
| `search integration tests (OpenSearch features)` | `Tests / Search` |
| `runtime audit coverage` | `Tests / Audit` |

Keep `GitGuardian Security Checks` and `CodeRabbit` under their existing names.
The GitHub Apps provide those contexts independently of the Actions workflows.
Require all replacement contexts to report success for the pull request's current
head before merging. The database and search summaries fail when any child job fails.

## Validate before production

Run the [search acceptance procedure](search.md#validate-a-candidate-in-github-actions)
for the candidate ref. The manual workflow includes the large pagination case that
ordinary PR regression jobs exclude. Configs must also verify the QA deployment of
the same candidate before production release.
