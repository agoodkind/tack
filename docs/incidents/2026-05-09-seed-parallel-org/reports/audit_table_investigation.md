# Audit ledger investigation, production CT 117

## 1. Verdict

**Present but inactive (partial).** The `audit` schema, all four tables, all weekly partitions, the chain heads, and 342k historical events exist and are correct. The state-change write path is still committing rows. The read-class verb path through the WAL has been silently dropping events since approximately `2026-05-09T05:57:31Z` (about 10.5 hours before this investigation). The cause appears to be a stuck idle WAL segment that triggers the WAL overflow guard, so every read-class event since then has been counted in `audit.wal.dropped` rather than appended.

The earlier subagent's report that `audit.events does not exist` is likely incorrect: the table exists and is being read and written. It is possible the subagent connected to the wrong database, used a role without `audit_reader` privileges, or saw an empty 0-row count and read it as "missing".

## 2. Evidence summary

- `information_schema.tables` shows all four documented audit tables under schema `audit`, plus 9 weekly range partitions of `audit.events` covering 2026-04-27 to 2026-06-29 (production query result, this investigation).
- `public.goose_db_version` records `version_id = 2` applied at `2026-05-05 02:34:08.943310 UTC`, confirming `migrations/002_audit.sql` was applied once and never rolled back (production query result).
- Row counts: `audit.events = 342,162`, `audit.chain_heads = 514`, `audit.notarizations = 29,918`, `audit.pii = 339,371`. Hash chain is being maintained and signed (production query result).
- 18 distinct verbs, 3 distinct orgs, oldest event `2026-04-28 16:37:36 UTC`, newest event `2026-05-09 06:29:09 UTC`. No write activity for the past ~10 hours despite live MCP traffic (production query result).
- Container env in `/root/tack/docker-compose.yml` and `/root/tack/.env` sets `AUDIT_WRITER_DSN`, `AUDIT_READER_DSN`, `AUDIT_REDACTOR_DSN`, `AUDIT_SIGNING_KEY_PATH`, and `AUDIT_WAL_DIR`. None are empty.
- App startup logs at `2026-05-09T04:30:54Z` emit `audit.writer_connected`, `audit.wal_enabled`, `audit.notarizer_started`, `audit.reader_connected`. Notarizer continues to sign every minute (`audit.notarizer.signed` log lines are still arriving as of 16:33 UTC).
- App logs from 04:30 through 05:57 show steady `audit.reconciler.flushed` activity, the most recent at `2026-05-09T05:57:31Z` flushing `20260509T055730.wal`. After that timestamp, no further `audit.reconciler.flushed` log line appears in the last 12 hours.
- App logs from `2026-05-09T16:04:31Z` onward emit `audit.wal.dropped` with `dropped_total` counting up: 10, 11, 12, ..., 39 over the window 16:04 to 16:31 UTC. Each drop is paired with an inbound `mcp.tool.started` for a read-class verb (`auth.token_used`, `node.read`, `node.list`, `workspace.list`, `audit.read`).
- One stale WAL segment exists at `/var/lib/tack/audit-wal/20260509T055731.wal`, 2,749 bytes, `mtime = 2026-05-09T05:57:31Z`. No other segments. This is the file the WAL overflow guard is tripping on.
- The single `node.create` event recorded at `06:29:09` after the WAL stall confirms state-change verbs still land (they bypass the WAL via `wal.go:118-120`); only read-class verbs are being dropped.

## 3. Schema and migration state

- `goose_db_version` rows: `0 (init)` at 02:34:06, `1 (001_schema.sql)` at 02:34:07, `2 (002_audit.sql)` at 02:34:08, all on 2026-05-05.
- `audit.events` is partitioned by `RANGE(event_time)` and currently has 9 weekly partitions named `events_2026_04_27` through `events_2026_06_29`, matching the `audit.ensure_events_partition` bootstrap that the migration runs for `base + 0..8 weeks` (`migrations/002_audit.sql:86-95`).
- `audit.chain_heads`, `audit.notarizations`, and `audit.pii` are all present as `BASE TABLE`.
- All four audit roles (`audit_writer`, `audit_reader`, `audit_redactor`, `audit_archiver`) appear to be active because the writer, reader, and notarizer all connected at startup with their respective DSNs.

## 4. Recorder code path analysis

- `cmd/server/main.go:130-131` calls `setupAuditRuntime(ctx, cfg)` and defers `Close`.
- `cmd/server/main.go:316-347` `buildAuditRecorder` opens `YBRecorder` (`internal/audit/yugabyte.go:34-52`) when `AUDIT_WRITER_DSN` is set, and then wraps it in `WALRecorder` (`internal/audit/wal.go:79-109`) when `AUDIT_WAL_DIR` is non-empty. Both env vars are set in production, so the runtime path is `SuppressingRecorder -> WALRecorder -> YBRecorder`.
- `internal/audit/wal.go:114-131` `WALRecorder.Record`:
  - State-change verbs (`IsStateChange(Verb(ev.Verb))`, see `internal/audit/verbs.go`) bypass the WAL and call the inner `YBRecorder` synchronously. This is why `node.create` at 06:29 still landed.
  - Read-class verbs go through `w.atOverflow()` first, then `w.append`. When `atOverflow` is true, the event is counted and dropped silently with a `WARN` log and a `telemetry.IncAuditDropped(verb, "wal_overflow")` bump.
- `internal/audit/wal.go:233-252` `atOverflow` walks `w.dir` and returns true if any `*.wal` file is older than `now - maxLag`. Default `maxLag` is `30 * time.Minute` (`wal.go:89-91`).
- `internal/audit/wal.go:283-320` `drainOnce` reads the directory, sorts segments, and skips the segment whose name matches `w.current.path`. There is no logic to force-rotate an idle current segment, so on quiet traffic the active segment can age past `maxLag` and never be drained.
- Once the active segment is older than 30 min, every subsequent read-class event hits `atOverflow == true`, gets dropped, and never causes a rotation either, so the system remains stuck until the process restarts or a state-change verb fires (state-change verbs bypass the WAL entirely and do not rotate it either).
- This is consistent with the observed behaviour: last successful WAL flush at `05:57:31`, first observed drop log at `16:04:31`. The gap between them is roughly the time from the last flushed segment until the active idle segment crossed `maxLag` plus the next traffic burst.

## 5. App log evidence

- `audit.writer_connected`, `audit.wal_enabled`, `audit.notarizer_started`, `audit.reader_connected` all logged at `2026-05-09T04:30:54Z`.
- `audit.notarizer.signed` lines are emitted continuously every minute for both org `00000000-0000-0000-0000-000000000000` (the system org) and org `019dc5ad-0408-7e43-9c4d-d3e6736ac058`. Most recent in capture: `2026-05-09T16:33:54Z`.
- `audit.reconciler.flushed` lines stop at `2026-05-09T05:57:31Z`. The last flushed segment was `20260509T055730.wal`.
- `audit.wal.dropped` lines start appearing at `2026-05-09T16:04:31Z` (after the first inbound MCP request post-quiet-period). 39 drops counted in the last 15 hours. Each carries `verb` matching a read-class verb (`auth.token_used`, `node.read`, etc.).
- No `audit.wal.replay_failed`, `audit.wal.scan_failed`, `audit.wal.parse_failed`, `audit.wal.open_failed`, or `audit.wal.remove_failed` errors anywhere in the last 12 hours, so the drainer is healthy; it just has nothing to drain because the active segment never rotates.
- No `audit.writer_disabled`, `audit.writer_setup_failed`, `audit.wal_setup_failed`, or `audit.dropped` (yb-side) events. The Yugabyte path itself has no errors.

## 6. Git history relevant to the audit subsystem

- `migrations/002_audit.sql` was added in commit `7480cc6 TACK-169: audit ledger schema, roles, and seed tooling` and has had no subsequent commits. `git log --diff-filter=D -- '*audit*'` returns nothing, so no audit file has been deleted from any branch.
- `internal/audit/wal.go` has two commits: `e99284c TACK-172: WAL for read audits` and `1a4450e audit logging with request and trace IDs`. No fix for the idle-segment overflow case has landed.
- TACK-169 through TACK-178 are recorded as Done, but the WAL idle-rotation bug is not covered by any of those tickets.

## 7. Implications for compliance

- The append-only ledger itself is intact. The hash chain has not been broken: `chain_heads` is still being updated by state-change writes, and the notarizer is still signing Merkle roots every minute.
- Loss is bounded to read-class verbs (mostly `auth.token_used` and `node.read`-class lookups) on this single deployment, since `2026-05-09T05:57:31Z`.
- Per-tenant scope: the lost events are concentrated on whichever orgs were issuing read traffic during the window. From the dropped log lines, every observation was for `user_id = 14385627-0313-50f8-bf7e-0c966e355dd9`.
- For SOC2 / SOX / HIPAA "who read what when" reporting, that 10-hour window has no record of who looked at what nodes. State-change auditability is preserved.
- The drop is observable (`audit.wal.dropped` WARN log + `IncAuditDropped` counter), so it is loud enough to alert on if the operator wires alerting to either signal. Currently no alerting appears to be configured.

## 8. Recommended next action for the operator

1. Restart the `tack-app-1` container to clear the stuck `20260509T055731.wal` segment. The drainer's `Close` path runs `drainOnce` after closing the active segment (`wal.go:137-153`), so a graceful restart will flush the 2.7 KiB of read events captured before 05:57:31 into `audit.events`. After restart the WAL will start a fresh segment and drain normally.
2. Treat this as a real bug, not a config issue. The WAL needs one of (a) a periodic forced rotation of the active segment when it ages past some idle threshold, or (b) inclusion of the active segment in `drainOnce` once it is older than `drainInterval` (with appropriate locking), or (c) `atOverflow` ignoring the file currently bound to `w.current` so the drop guard does not trip on the very file we are using. Recommend opening a follow-up ticket on TACK-172.
3. Add an alert on the `audit.wal.dropped` WARN line and on `IncAuditDropped("*", "wal_overflow")` so the next occurrence is caught within minutes rather than hours.
4. Do not run any `audit.events` migration. The schema is correct. Do not restore from any earlier backup. The historical ledger is intact.
5. Communicate to anyone consuming audit query tooling that the window `2026-05-09T05:57:31Z` to the time of restart will have a partial view of read-class verbs only.
