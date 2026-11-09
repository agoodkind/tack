# Tack documentation

## Architecture

- [Tack design requirements](../README.md#design-requirements)
- [Tack project contract](../AGENTS.md)
- [Find current sources](runbooks/find-current-sources.md)
- [Run tests](runbooks/run-tests.md)

## Audit

- [Complete audit recording](reference/audit/2026-10-21-audit-completeness-design.md)
- [Audit recording lifecycle and formats](reference/audit/recording-lifecycle.md)
- [Audit recording acceptance requirements](reference/audit/recording-acceptance.md)
- [Operator identity and audit recording](reference/audit/operator-identity.md)
- [Audit scale requirements](reference/audit/horizontal-design.md)
- [How audit delivery uses Kafka](explanation/audit/audit-kafka-cutover.md)
- [How audit partitions stay available](explanation/audit/audit-partition-manager-design.md)

## Recovery

- [Backup and recovery tiers](explanation/backup-tiers.md)
- [Resilience constraints](reference/recovery/2026-08-06-backup-rearch-design.md)
- [Backup and resilience acceptance criteria](reference/recovery/2026-08-08-backup-rearch-acceptance.md)
- [Backup retention contract](reference/recovery/2026-10-23-backup-retention-design.md)
- [Check backup age](runbooks/recovery/check-backup-age.md)
- [Repeat a backup alarm deliberately](runbooks/recovery/repeat-backup-alarm.md)
- [Expire old backups](runbooks/recovery/expire-backups.md)
- [Prepare a data restore](runbooks/recovery/restore-data.md)
- [Verify backup restores](runbooks/recovery/verify-restores.md)
- [Measure guest-failure recovery](runbooks/recovery/measure-guest-failure-recovery.md)
- [Verify ledger node changes](runbooks/recovery/verify-ledger-node-changes.md)

## Search

- [How search uses source data and indexed pages](explanation/search/search-design.md)
- [Search contracts](reference/search/2026-09-19-search-design.md)
- [Search content contracts](reference/search/content-contracts.md)
- [Search work contracts](reference/search/work-contracts.md)
- [MCP output and search operations](reference/search/search.md)
- [Search acceptance criteria](reference/search/2026-09-19-search-acceptance.md)
- [Search cluster and capacity criteria](reference/search/cluster-capacity-criteria.md)
- [Run search acceptance tests](runbooks/search/run-acceptance-tests.md)
- [Apply missing search projection declarations](runbooks/search/implement-opensearch-metadata.md)
- [Verify search projection corrections after activation](runbooks/search/verify-projection-corrections.md)
- [Validate a search correction](runbooks/search/implement-search-correction.md)
- [Release and validate search](runbooks/search/implement-opensearch-release.md)
- [Add production search capacity](runbooks/search/add-production-capacity.md)

## Nodes

- [How node references stay unique](explanation/nodes/node-reference-uniqueness-design.md)
- [Reference uniqueness requirements](reference/nodes/2026-08-05-node-reference-uniqueness-design.md)
- [Node integrity requirements](reference/nodes/2026-10-16-node-integrity.md)
- [Node repair manifests and reviewed mappings](reference/nodes/2026-10-16-node-backfill.md)
- [Frozen node repair manifests](reference/nodes/repair-manifests.md)
- [Activate strict node validation](runbooks/nodes/activate-strict-validation.md)

## Deployment

- [Provision an environment](runbooks/deployment/provision-environment.md)
- [Reset Tack QA and regenerate its data](runbooks/deployment/reset-qa.md)
- [Verify a three-guest product store](runbooks/deployment/implement-store-three-data-guests.md)
- [YugabyteDB networking contract](reference/deployment/yugabyte-networking.md)
- [YugabyteDB port checks on IPv6-only networks](explanation/deployment/yugabyte-overlay.md)
- [Update the YugabyteDB image](runbooks/deployment/update-yugabyte-image.md)
- [Environment validation](explanation/deployment/env-var-remediation.md)
- [Deployment capacity inputs](reference/deployment/capacity.md)

## Providers

- [Tack provider contract](reference/tack-provider.md)
- [PostgreSQL provider role locking](reference/postgresql-provider-locking.md)
- [Configure the PostgreSQL provider for YugabyteDB](runbooks/configure-postgresql-provider.md)
