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

// ops qa datagen reports existing events for these verbs because it cannot
// run the host ledger commands or service jobs that record them.
var datagenLedgerVerbs = []string{
	string(audit.VerbOpsLedgerNodePrepare),
	string(audit.VerbOpsLedgerNodeWait),
	string(audit.VerbOpsLedgerBootstrapWait),
	string(audit.VerbOpsLedgerAuditBootstrap),
	string(audit.VerbSearchIndexStructure),
	string(audit.VerbSearchAccessRolloutPhase),
	string(audit.VerbSearchModelRepair),
	string(audit.VerbAuditPartitionMaintain),
	string(audit.VerbNodeSubtreeDeleteResume),
}

// datagenLedgerVerb is the row count and newest event time of one verb on the
// system organization.
type datagenLedgerVerb struct {
	Verb          string `json:"verb"`
	Rows          int64  `json:"rows"`
	LatestEventAt string `json:"latest_event_time,omitempty"`
}

// datagenLedgerVerbsReport lists the row count of every requested verb on
// the system organization, including zero counts.
type datagenLedgerVerbsReport struct {
	Command string              `json:"command"`
	ReadAt  string              `json:"read_at"`
	Verbs   []datagenLedgerVerb `json:"verbs"`
}

// datagenSearchAccessVerbs lists the audit verbs of the access rollout
// and rollback commands. The ops qa datagen command cannot run those
// operator commands. The seed reports the rows those commands wrote.
var datagenSearchAccessVerbs = []string{
	string(audit.VerbOpsSearchAccessRollout),
	string(audit.VerbOpsSearchAccessRollback),
}

// readDatagenLedgerVerbs reads the presence of each host-only ledger verb
// through AUDIT_READER_DSN. It records nothing of its own.
func readDatagenLedgerVerbs(ctx context.Context, f *cli.Factory) (datagenLedgerVerbsReport, error) {
	return readDatagenVerbPresence(ctx, f, "ops.qa.datagen.ledger_verbs", datagenLedgerVerbs)
}

// readDatagenSearchAccessVerbs reads the presence of each access rollout
// and rollback verb through AUDIT_READER_DSN. It does not record events
// of its own.
func readDatagenSearchAccessVerbs(ctx context.Context, f *cli.Factory) (datagenLedgerVerbsReport, error) {
	return readDatagenVerbPresence(ctx, f, "ops.qa.datagen.search_access_verbs", datagenSearchAccessVerbs)
}

// readDatagenVerbPresence reads the row count and newest event time of
// each verb in verbs on the system organization. It uses command as the
// report label.
func readDatagenVerbPresence(ctx context.Context, f *cli.Factory, command string, verbs []string) (datagenLedgerVerbsReport, error) {
	report := datagenLedgerVerbsReport{
		Command: command, ReadAt: clock.Now().UTC().Format(time.RFC3339Nano), Verbs: nil,
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
	presence, err := reader.VerbPresence(ctx, audit.SystemOrgID(), verbs)
	if err != nil {
		wrapped := fmt.Errorf("read the ledger verb presence: %w", err)
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
