# Backup incidents and earlier recovery decisions

The recovery records in the November 7, 2026 source snapshot report the following incidents and measurements. These records do not establish current deployment state.

## Backup failures

The earlier plans distinguish an accidental backup from a backup that stopped without an alarm.

Incident records:

| Date | Recorded behavior | Recorded effect |
| --- | --- | --- |
| July 29, 2026 | The backup bucket exhausted its single 1 GB volume while the disk still had 91 GB free. | Production backups stopped, and the system did not report the fault for five weeks. |
| August 5, 2026 | Bare `ops backup` silently ran a full production snapshot. The phase 1 plan classified the incident as S0. | A full-table scan grew the YugabyteDB tablet-server process to 13.7 GiB and reduced the memory available for the guest page cache. |

The [phase 1 plan](https://github.com/agoodkind/tack/blob/7e469f9851c55e0ae36a03fe279287c6a430da05/docs/superpowers/plans/2026-08-06-backup-rearch-phase1.md) proposed an 8 GiB process limit, a 2 GiB block cache, and a 10 GiB container limit. It expected container recreation to release the enlarged process. Those statements record the proposed recovery rather than prove that an operator completed it.

The [acceptance record](https://github.com/agoodkind/tack/blob/7e469f9851c55e0ae36a03fe279287c6a430da05/docs/superpowers/specs/2026-08-08-backup-rearch-acceptance.md) also reports these failures:

- A YugabyteDB 2025.2 scratch engine refused the 2024.2 catalog signature in the first export that included privileges. The rehearsal could not restore that export successfully.
- A 30-minute check cadence missed the required 15-minute alarm bound by 14 minutes and 56 seconds. The record included the check interval and randomized delay in that bound.

## Signing and broker decisions

The [recovery design](https://github.com/agoodkind/tack/blob/7e469f9851c55e0ae36a03fe279287c6a430da05/docs/superpowers/specs/2026-08-06-backup-rearch-design.md) reversed an August 8, 2026 decision to share one vault-stored signing key per environment. It instead required a key per host and an accepted signer set per environment. The recorded rationale distinguishes key isolation from trust: a signer writes its own identifier, and an identifier alone cannot authorize a newly generated key or support revocation.

The same design reported static Kafka membership and did not identify a proven online procedure to expand it from one member to three. It retained two candidate procedures: upgrade to dynamic membership before adding brokers, or rebuild three static members during maintenance. It required QA proof before selecting either procedure. The historical record does not establish which procedure an operator later used.
