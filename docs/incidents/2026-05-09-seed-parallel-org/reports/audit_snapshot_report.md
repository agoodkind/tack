# Audit snapshot report

Captured at 20260509T164222Z (UTC). Source: production CT 117 (`3d06:bad:b01::117`), `tack-app-1` and `tack-yugabyte-1` on database `tack`.

## 1. Verdict

Snapshot complete and verified. No production mutations. App container untouched. Tarball SHA-256 matches end-to-end (CT 117 to local).

One correction worth surfacing: an earlier inspection step initially queried database `yugabyte` and found no `audit` schema. The audit tables actually live in database `tack` (per `AUDIT_WRITER_DSN`). Once corrected, prior counts confirmed.

## 2. Snapshot paths

- CT 117: `/root/audit-snapshots/20260509T164222Z/`
- CT 117 tarball: `/root/audit-snapshots/audit-snapshot-20260509T164222Z.tar.gz` (71 MB)
- Local: `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/audit-snapshot-20260509T164222Z.tar.gz`

## 3. SHA-256 manifest

```
bdd834830d17dc5a898e46de0f269ae7f8d5157bd78e30eec2a305f95ca7439e  audit-snapshot-20260509T164222Z.tar.gz (CT 117 == local)

5e154a7a1de266973c8bb264f05a96a91bc86d87c7a495bf05b1f7307a15a00d  wal/20260509T055731.wal                       (2,749 B; container source SHA matches)
d43c794de31506d12132a998022578eb5eec84e4ad7f21b69a4a05583e383b3b  sql/events.csv                                (243,081,132 B)
236792399139b76a2014fcf6df38a89e3e2063afa084384f2f7c559d0cd052a8  sql/chain_heads.csv                           (72,573 B)
a37f66502dee90c2c065f25b51b06978fc5051e97861f64d89fbbbac2a6caa3f  sql/notarizations.csv                         (784,558,083 B)
0000fb777b673796776363b049c80a24fd38cef791a4a56bfe3183f89b7079a1  sql/pii.csv                                   (86,309,948 B)
f0c10dba7936f40a66a79d8417cc8315d13bd1fbb6301737de0a2da16f2896b5  sql/goose_db_version.csv                      (130 B)
```

Full per-file manifest at `MANIFEST.txt` inside the tarball.

## 4. WAL file analysis

Single buffered segment `20260509T055731.wal`, 2,749 bytes. Parsed cleanly with the `[4-byte BE length][JSON Event]` framing from `internal/audit/wal.go`.

- Records: 4
- Bytes consumed: 2,749 / 2,749 (no truncation, no parse error)
- Verb distribution: `auth.token_used` = 2, `node.list` = 1, `node.search` = 1
- All four are read-class verbs (consistent with the WAL routing rule in `WALRecorder.Record`).
- All outcomes: `ok`
- Time range: 2026-05-09T05:57:31.312665295Z to 2026-05-09T05:57:31.704372677Z (about 392 ms)

These four read events have been buffered on disk since 05:57 UTC and have not been drained, suggesting the drainer has not run a successful pass on this segment since it was created. The segment is still the active one (no rotation has happened), which matches the `ensureSegmentLocked` behavior of holding the active minute open until rotation.

## 5. Audit table row counts (database `tack`)

Captured at the moment of dump (before tarball):

| Table                | Row count |
|---|---|
| `audit.events`        | 342,162 |
| `audit.chain_heads`   | 514 |
| `audit.notarizations` | 29,954 (count) / 29,957 (CSV) |
| `audit.pii`           | 339,371 |

`audit.events` is partitioned by week. Partition distribution:
- `events_2026_04_27` = 219,195
- `events_2026_05_04` = 122,967
- `events_2026_05_11` and later = 0

The 3-row drift between the `count(*)` reading and the streamed CSV for `audit.notarizations` is expected because the notarizer is actively signing during the dump (~3 orgs/minute). All other tables were stable across the dump window.

Post-snapshot recheck (about 5 minutes later) showed `audit.notarizations` had advanced to 29,963, again consistent with steady notarizer activity. The other three tables remained at the same counts.

## 6. Goose migration state

- `public.goose_db_version` exists in database `tack` and has 3 rows.
- Migration `version_id=2` is applied: `is_applied=t`, `tstamp=2026-05-05 02:34:08.94331`.
- Schema state for the `audit` package is therefore on the `002_audit.sql` baseline.

## 7. Notarizer recent activity

180 `audit.notarizer.signed` log lines in the last 60 minutes. Three distinct orgs are being notarized once per minute. Latest signed event per org at snapshot time:

```
00000000-0000-0000-0000-000000000000  16:44:54.065Z  shard_count=256
019dc5ad-0408-7e43-9c4d-d3e6736ac058  16:44:54.068Z  shard_count=255
3dc1c593-35ea-5214-a198-800e9f38916a  16:44:54.069Z  shard_count=3
```

All three sign with `key_id=ed25519:f04a5d764fecb815`. Notarizer is healthy.

## 8. Production health

Pre-snapshot:
- All seven Compose containers `Up`. `tack-yugabyte-1`, `tack-fdb-1`, `tack-meilisearch-1`, `tack-temporal-1`, `tack-temporal-db-1` reporting `(healthy)`. `tack-app-1` `Up 12 hours`.

Post-snapshot recheck:
- Same container states.
- WAL directory unchanged: same single file, same size (2,749 B), same mtime (May 9 05:57).
- Recent app errors: a small cluster of `mcp.tool.failed` WARN lines for `tack_search` and `tack_describe_workspace` between 16:40 and 16:41Z. These predate the snapshot operation and look like pre-existing MCP tool failures from request_id `20faf8e5-...` and similar, not snapshot-induced.
- Notarizer continued signing on the minute boundary throughout (observed 16:43, 16:44, 16:45, 16:46Z).

No evidence of impact from this read-only snapshot.

## 9. Operator next-step recommendation

It looks safe to proceed with a controlled restart of `tack-app-1`, with caveats:

- The four WAL records are buffered on disk. On a clean shutdown, `WALRecorder.Close()` runs a final `drainOnce` pass that ships them through to the inner Recorder. If shutdown is unclean (SIGKILL, OOM, host crash), the segment stays on disk and the next startup will drain it on the next minute tick. Either path is recoverable, since the source-of-truth is the file we just snapshotted.
- The active segment naming uses the minute-level timestamp at first append. Once the app restarts and a new event is appended, a new segment will open under a new name. The 05:57 segment will sit alongside it until drained, then be removed.
- It may be worth doing a SIGTERM (graceful) restart rather than `docker kill`, so the existing buffered events drain into `audit.events` and `audit.pii` before the process exits. After that, the local tarball plus row counts should still let you reconstruct the same picture if anything is questioned.
- Database state is in good shape: events partitioning is healthy, chain_heads has all 514 entries, notarizations is current, goose 002 confirmed applied. Nothing in the audit subsystem looks broken or mid-write.

If you want extra caution, you could optionally run a second post-restart snapshot of the four tables and diff row counts; that would confirm WAL drain semantics on this exact build (`dd430c9 built 2026-05-09T04:27:25Z`).

Verdict: clear to restart, with a graceful shutdown preferred so the four buffered read events drain before exit.
