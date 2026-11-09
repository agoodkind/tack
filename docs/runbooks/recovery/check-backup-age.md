# Check backup age

The staleness check records observations and can send alarm mail. Run checks
through the audited operator CLI with a verified identity.

1. Use the Configs-managed installation and backup settings. Confirm the alarm
   recipient and relay configuration. An empty `TACK_BACKUP_ALARM_EMAIL` disables
   mail; a nonzero check result still reports stale protection.
2. Run `ops backup staleness-check` through `tack-ops` with the required
   execution flag and operator identity. Record the output and exit status.
   The command returns a nonzero status when any mechanism exceeds its own
   threshold. A report-write failure can also fail the command.
3. Read each mechanism's age, threshold, verdict, and evidence separately.
   The report expresses `age` and `threshold` as whole seconds. Compare the
   observed age with the corresponding `TACK_BACKUP_STALENESS_*_MAX_SECONDS`
   setting from [server configuration](../../../internal/config/config.go).
   Continuous-backup mode must enable the FoundationDB metric; a report that
   omits it does not establish product-backup freshness.

   The report identifies the source of each measurement.

   Measurement sources:

   | Metric | Last success |
   | --- | --- |
   | `ledger-export` | The newest complete export run's UTC timestamp. |
   | `rehearsal` | The last passing drill marker in object storage. |
   | `replication` | The most recent observation with every ledger node alive and every tablet fully replicated. |
   | `fdb-restorable` | The continuous backup's newest restorable point. |

4. Treat `age=unknown` as stale. Read the cause before diagnosing the mechanism.
   A mechanism that has never succeeded differs from one that the checker
   cannot read. For an unreadable probe, the checker can date the age from its
   last local success; the report retains the failed-probe reason and the time
   of that earlier reading. A remembered reading does not clear an existing
   alarm.
5. Compare the observed export and rehearsal ages with their actual timer schedules. Include randomized delay and archive completion time. Do not assume that every threshold detects one missed run. The examples compare missed daily runs with each threshold.

   Schedule examples:

   | Mechanism | Schedule | Threshold | Missed-run behavior |
   | --- | --- | --- | --- |
   | Rehearsal | Daily. | Eight days. | Seven scheduled runs can fail to occur before age exceeds the threshold. |
   | Export | Daily. | 36 hours. | One missed run can trigger the alarm before the next scheduled run. |
6. Inspect the guest journal for the individual readings, alarm decisions, and
   mail transport errors. The first stale transition attempts one plain-language
   email. Its subject identifies the guest and the fault or fault count. The checker retries refused mail. Continued
   staleness does not repeat an accepted message, and recovery does not send
   mail. A successful fresh probe clears the remembered fault.
7. Inspect primary and deputy behavior when investigating duplicate or missing
   mail. A deputy checks the audit ledger for a recent primary-service command
   and retries that observation after its grace period when necessary. A recent
   primary run defers the deputy's new alarm. An absent or unreadable primary
   record permits the deputy to send it.

The check records FoundationDB version-to-time readings in object storage for
recovery after source-cluster loss. A failed version-log write does not invalidate
the observed restorable point; inspect that write failure separately when a
recovery lookup lacks a reading.

## Read a sample report

This example shows a recent export, a passing drill, and a fully replicated ledger.

```
ledger-export age=11520s threshold=129600s FRESH newest complete run 20260829T010000Z
rehearsal     age=7200s threshold=691200s FRESH restore drill rt20260829T060000Z-42 passed: fdb, yugabyte
replication   age=0s threshold=1800s FRESH 0 dead nodes, 0 under-replicated tablets
```
