package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
)

// datagenLedgerVerbs are the audit verbs that only host-run operator commands
// record. `ops qa datagen` cannot run those commands: the app service mounts
// no Docker socket for the yb-admin one-shots, audit-bootstrap refuses a
// populated ledger, and a break-glass plan mails the alarm address and runs
// its statements as the engine superuser in tack-ops. The seed therefore
// reports the rows the real commands wrote (AGENTS.md rule 4).
var datagenLedgerVerbs = []string{
	string(audit.VerbOpsLedgerNodePrepare),
	string(audit.VerbOpsLedgerNodeWait),
	string(audit.VerbOpsLedgerBootstrapWait),
	string(audit.VerbOpsLedgerAuditBootstrap),
	string(audit.VerbOpsDBPlanOpen),
	string(audit.VerbOpsDBPlanClose),
}

// datagenLedgerVerb is the row count and newest event time of one verb on the
// system organization.
type datagenLedgerVerb struct {
	Verb          string `json:"verb"`
	Rows          int64  `json:"rows"`
	LatestEventAt string `json:"latest_event_time,omitempty"`
}

// datagenLedgerVerbsReport lists every host-only ledger verb, with zero
// counts included.
type datagenLedgerVerbsReport struct {
	Command string              `json:"command"`
	ReadAt  string              `json:"read_at"`
	Verbs   []datagenLedgerVerb `json:"verbs"`
}

// readDatagenLedgerVerbs reads the presence of each host-only ledger verb
// through AUDIT_READER_DSN. It records nothing of its own.
func readDatagenLedgerVerbs(ctx context.Context, f *cli.Factory) (datagenLedgerVerbsReport, error) {
	report := datagenLedgerVerbsReport{
		Command: "ops.qa.datagen.ledger_verbs", ReadAt: clock.Now().UTC().Format(time.RFC3339Nano), Verbs: nil,
	}
	if strings.TrimSpace(f.Cfg.AuditReaderDSN) == "" {
		err := errors.New("the ledger verb report needs AUDIT_READER_DSN")
		slog.ErrorContext(ctx, "qa.datagen.ledger_verbs_config_missing", slog.String("err", err.Error()))
		return report, err
	}
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.ledger_verbs_reader_failed", slog.String("err", err.Error()))
		return report, fmt.Errorf("open the ledger reader: %w", err)
	}
	defer reader.Close()
	presence, err := reader.VerbPresence(ctx, audit.SystemOrgID(), datagenLedgerVerbs)
	if err != nil {
		wrapped := fmt.Errorf("read the host-only ledger verbs: %w", err)
		slog.ErrorContext(ctx, "qa.datagen.ledger_verbs_failed", slog.String("err", wrapped.Error()))
		return report, wrapped
	}
	for _, verb := range presence {
		entry := datagenLedgerVerb{Verb: verb.Verb, Rows: verb.Rows, LatestEventAt: ""}
		if verb.Latest != nil {
			entry.LatestEventAt = verb.Latest.UTC().Format(time.RFC3339Nano)
		}
		report.Verbs = append(report.Verbs, entry)
	}
	return report, nil
}
