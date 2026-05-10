# Wave 1 runbook revision report

Author: revision pass following `wave1_runbook_verification_report.md`.
Scope: edits to `docs/phase2-wave1-runbook.md` only. No source changes,
no deploys, no production touches. Plain English. No em dashes, no en
dashes, no bare double-hyphen sequences in prose.

The verification report flagged 30-ish defects in the runbook that
landed at commit `a5aec6d`. This revision fixes the load-bearing ones
and adds the verification gates that the report called out as missing.
The runbook still has its original 12-section structure (sections 0
through 11; section 0 is new and described below).

---

## 1. Per-defect fixes

### Defect 1: `scripts/audit-parity.sh` references

The script does not exist in the tree. The Go subcommand
`./server ops audit parity` at `internal/ops/audit_parity.go` is the
real parity gate. The shell-script moratorium also forbids creating
`audit-parity.sh`.

Three runbook locations referenced the script.

- Section 1 pre-flight (`docs/phase2-wave1-runbook.md` original line
  58-59). Was `test -x scripts/audit-parity.sh && echo OK`. Now reads:
  `bash -c "cd /Users/agoodkind/Sites/tack && /opt/homebrew/bin/go build ./cmd/server/ && ./server ops audit help"`,
  with a note that the help output must list `parity`. Citation:
  `internal/ops/audit_parity.go` for the implementation, and
  `internal/ops/command.go` for the dispatcher.
- Section 6 Test 2 (original line 327-335). Was
  `bash /root/tack/scripts/audit-parity.sh --window=10m`. Now uses
  `docker compose exec -e TACK_PARITY_FROM=... -e TACK_PARITY_TO=... -e TACK_PARITY_THRESHOLD=1.0 app /server ops audit parity`
  with the BSD `date -u -v-10M` form spelled out and a GNU `date -d`
  alternative. Citations: env var names from
  `internal/ops/audit_parity.go:24-27`, threshold default from
  `internal/ops/audit_parity.go:170-173`.
- Section 7 hourly soak loop (original line 347-352). Was
  `bash /root/tack/scripts/audit-parity.sh --window=1h`. Now a `while
  true` loop that recomputes `TACK_PARITY_FROM` and `TACK_PARITY_TO`
  per iteration and runs the same `docker compose exec` form.

### Defect 2: `rsync -az dist/tack tack:/root/tack/dist/tack-wave1` in section 4 step 3

The user has banned hand-rolled `rsync` of host-built binaries to
production, and there is no Makefile precedent for shipping a
pre-built `dist/tack` (the deploy contract at `Makefile:123-135`
builds inside the remote).

Was:
```
make build
rsync -az dist/tack tack:/root/tack/dist/tack-wave1
ssh tack 'cd /root/tack && DATABASE_URL=$(grep ^AUDIT_WRITER_DSN .env | cut -d= -f2-) ./dist/tack-wave1 migrate'
```

Now:
```
make deploy
ssh tack 'cd /root/tack && docker compose run --rm --entrypoint /server app migrate'
```

The new form runs the migration in an ephemeral container off the
freshly-built `tack-server:latest` image, so the schema and binary
line up. The runbook also notes that `./server ops deploy` will
replace `make deploy` once the consolidation lands. Citations:
`Makefile:123-135` for the existing deploy contract,
`incident_2026-05-09_seed_parallel_org/ops_consolidation_plan.md:1-50`
for the future state.

### Defect 3: `rsync -az --delete ... . tack:/root/tack/` in section 5 step 1

Same source-rsync ban applies. Section 5 step 1 was a hand-rolled
rsync that duplicated `Makefile:129` and skipped the `make
deploy-preflight` and `make backup` prerequisites that the Makefile
target pulls in.

Was a multi-flag `rsync` invocation. Now reads `make deploy` with a
short paragraph explaining what `make deploy` does and does not pull
(builds the app image; does not pull `kafka`, `seaweedfs`,
`clickhouse` images, which is why step 2 still pulls them
explicitly). Same future-state note pointing at
`./server ops deploy`.

Section 5 step 5 originally also called `make deploy` (a duplicate).
That has been removed; step 5 now starts the consumer container off
the image that step 1 already built. Citation: `Makefile:123-135`.

### Defect 4: verb-name drift `tack.workspace.listed`

The real verb is `workspace.list`. Citation:
`internal/audit/verbs.go:38` (`VerbWorkspaceList Verb = "workspace.list"`).
There is no `tack.*` prefix and no past-tense `.listed` form anywhere
in the codebase.

Section 6 originally read "The `tack.workspace.listed` (or equivalent
verb name; the canonical list lives in `internal/audit`) row counts
must match between the two tables." Now reads "The `workspace.list`
row counts (the verb constant `VerbWorkspaceList = "workspace.list"`
at `internal/audit/verbs.go:38`; there is no `tack.*` prefix anywhere
in the codebase) must match between the two tables."

Section 10 originally referenced `tack.issue.*` rows in the
schema-mismatch failure mode. Updated to `node.create` (the actual
verb constant `VerbNodeCreate Verb = "node.create"` at
`internal/audit/verbs.go:12`).

### Defect 5: log-line drift

Six distinct log-line strings drifted from the binary. Each fix below
cites the real emission site.

- `audit.recorder.dual_kafka_wal` does not exist. The real line is
  `audit.kafka_enabled` at `cmd/server/main.go:399`. Section 5 step 6
  updated; the runbook now greps for `audit\.kafka_enabled` and
  `audit\.wal_enabled` and explicitly notes that the
  `audit.recorder.dual_kafka_wal` line is not in the binary.
- `audit.recorder.wal_only` does not exist. The real WAL-only line is
  `audit.wal_enabled` at `cmd/server/main.go:371`. Section 9 rollback
  updated; the runbook now confirms `audit.wal_enabled` and the
  absence of a fresh `audit.kafka_enabled` after restart.
- `audit.consumer.stalled` does not exist. The real lag warning is
  `consumer.lag.high` at `internal/audit/consumer.go:329`. Section 8
  closeout grep updated. Section 10 lag row updated.
- `audit.kafka.produce_failed` does not exist. The real produce-error
  line is `kafka.produce.failed` (no `audit.` prefix) at
  `internal/audit/kafka_recorder.go:130`. Section 8 closeout grep
  updated; section 10 broker-down row updated.
- `audit.consumer.batch_lag_high` and `audit.consumer.clickhouse_unavailable`
  do not exist. Section 10 lag row updated to `consumer.lag.high`.
  Section 10 ClickHouse-down row updated to reference the
  `audit.consumer.clickhouse_*_failed` family per
  `internal/audit/consumer.go:178-192`.
- `audit.consumer.started` (with a dot) is what the runbook expected.
  The real line is `audit_consumer.started` (with an underscore) at
  `cmd/audit-consumer/main.go:103`. Section 5 step 5 updated; the
  runbook now greps for `audit_consumer.started` and explicitly tests
  the result with a `CONSUMER_STARTED` / `CONSUMER_NOT_STARTED` echo.
- `audit.notarizer.disabled` does not exist. Section 10 notarizer row
  updated to describe the actual gate at
  `internal/audit/consumer.go:131` (silent skip when
  `cfg.SigningKeyPath` is empty) and the
  `audit.consumer.notarizer_failed` line for malformed-key cases.

### Defect 6: metric-name drift

The runbook called out a Prometheus-shaped metric
`audit.kafka.produce_failed_total` and a `/metrics` scrape endpoint.
Tack uses `expvar` at `/debug/vars`, not Prometheus.

Real metric: the expvar map `tack_audit_kafka_produce_total` keyed by
`result="ok|error"` at `internal/telemetry/metrics.go:135` and
incremented at `:170-172`. Mounted at `/debug/vars` per
`cmd/server/main.go:284`.

Section 7 aux checks updated. The runbook now reads the expvar map
via `curl -sS http://localhost:8000/debug/vars | jq
".tack_audit_kafka_produce_total.error // 0"`. Section 8 closeout
also adds the same expvar cross-check alongside the log-line grep,
and section 10 broker-down row references the expvar bucket directly.

Section 7 also adds a one-time read of the
`tack_audit_dual_write_skew_seconds` histogram per the verification
report's gate 5. Citation:
`internal/telemetry/metrics.go:155-156`.

### Defect 7: missing verification gates

Four gates added per the verification report's section 6:

- Section 5 step 6 now includes an explicit `WRAP_ENGAGED /
  WRAP_NOT_ENGAGED` test for `audit.kafka_enabled` after the app
  restarts. Citation: `cmd/server/main.go:399`.
- Section 5 step 5 now includes a `CONSUMER_STARTED /
  CONSUMER_NOT_STARTED` test for `audit_consumer.started`. Citation:
  `cmd/audit-consumer/main.go:103`.
- Section 6 Test 1 now includes a
  `SELECT consumer_group, topic, partition, "offset", updated_at
  FROM audit.consumer_offsets ORDER BY updated_at DESC LIMIT 5`
  query to confirm the consumer is actually committing offsets.
- Section 6 Test 2 spells out the parity-window env var contract at
  first use, including a worked example with the BSD `date -u -v-10M`
  form and a GNU `date -u -d '10 minutes ago'` alternative.
  Citations: `internal/ops/audit_parity.go:22-27`.

The verification report's section 6 gate 8 (pre-restart `docker
compose stop --timeout 15 app` to drain the Kafka producer's
in-flight buffer) is also now in section 9. Citation:
`internal/audit/kafka_recorder.go:178`.

---

## 2. Sections added

### Section 0: Compatibility note

A new section directly above section 1 records that the runbook
reflects the deploy mechanics in place as of commit `a5aec6d`. It
states:

- The current sanctioned deploy path is `make deploy`. Hand-rolled
  `rsync` against production is forbidden.
- When `./server ops deploy` lands per the consolidation plan,
  sections 4 and 5 will need a follow-up revision. Cites
  `incident_2026-05-09_seed_parallel_org/ops_consolidation_plan.md`.
- The parity gate is the Go subcommand
  `./server ops audit parity` at `internal/ops/audit_parity.go`. The
  earlier `scripts/audit-parity.sh` never existed and is forbidden by
  the shell-script moratorium.

This section serves as the moratorium-acknowledged exception that the
verification report's section 5 open question 1 asked for.

### Section 10 additions

Two new rows added to the failure-modes table:

- "Producer in WAL-only after restart" with the
  `audit.wal_enabled` / no-`audit.kafka_enabled` symptom and the
  `audit.kafka_setup_failed` fallback case (`cmd/server/main.go:390`).
- "Dual-write divergence" with the `dual.write.divergence` warning
  (`internal/audit/dual.go:76`) and the
  `tack_audit_dual_write_total` map split between `primary` and
  `secondary` paths (`internal/telemetry/metrics.go:152-153`).

These give the operator named symptoms for the most common ways the
new dual-write path can silently degrade.

---

## 3. Verification-report items deliberately not changed

- **`make backup` shell-script-moratorium concern**. Section 1
  pre-flight and section 8 closeout still call `make backup`.
  Replacing it requires the not-yet-landed `./server ops backup`
  subcommand. The new section 0 covers the moratorium exception
  generally; per-step exceptions would duplicate that.
- **Section 2 `meta.properties` strictness**. The verification report
  called the existing command "fine in shape" and only offered an
  optional tightening. Left unchanged.
- **Section 3 signing-key owner claim**. The runbook says owner
  should match the audit-consumer container's UID; the verification
  report suggested softening to "any UID with read permission". Left
  unchanged because the existing wording is consistent with the
  read-only `/etc/tack` mount and the default `root` UID.
- **Open question 5 about `audit_consumer.started` log-key style**.
  This revision picked the binary's actual emission and did not
  patch source code per the "Do not modify any source code"
  constraint. The mismatch with the `noun.verb` convention in
  `CLAUDE.md` is a separate follow-up.
- **Open question 6 about ClickHouse health gating**. The runbook
  says ClickHouse must come up `healthy` (section 5 step 4) and that
  ClickHouse degradation is best-effort (section 10). Both are true
  at different lifecycle points; left unchanged.

---

## 4. Diff stat

Source: `git diff --stat docs/phase2-wave1-runbook.md` against
`HEAD` at the start of this revision pass.

```
docs/phase2-wave1-runbook.md | 287 ++++++++++++++++++++++++++---------
1 file changed, 224 insertions(+), 63 deletions(-)
```

Sections affected:

- Section 0 (added).
- Section 1 (pre-flight): one bullet replaced.
- Section 4 (migration order): step 3 rewritten; rsync removed,
  `docker compose run --rm` form added.
- Section 5 (deploy sequence): step 1 rewritten; bare `rsync`
  removed. Step 5 now starts the audit-consumer off the image step
  1 already built and adds a `CONSUMER_STARTED` gate. Step 6 grep
  pattern and `WRAP_ENGAGED` gate added.
- Section 6 (smoke test): Test 1 verb name corrected to
  `workspace.list`; new `audit.consumer_offsets` query added. Test 2
  rewritten around the Go subcommand and the `TACK_PARITY_FROM` /
  `TACK_PARITY_TO` env contract.
- Section 7 (parity gate / 24-hour soak): loop rewritten around the
  Go subcommand; expvar-based producer-error check added; dual-write
  skew check added; consumer-lag aux check now references the real
  `consumer.lag.high` line.
- Section 8 (exit criteria): closeout grep patterns updated to the
  real log keys; expvar cross-check added.
- Section 9 (rollback): pre-restart `stop --timeout 15` added; log
  verification updated to `audit.wal_enabled` / no
  `audit.kafka_enabled`.
- Section 10 (failure modes): table rows updated with real log,
  metric, and verb names; two new rows added (WAL-only after
  restart, dual-write divergence); notarizer bullet rewritten.
- Section 11 (deliberately not covered): unchanged.

Total lines in revised runbook: 658 (was 498 at `a5aec6d`).
