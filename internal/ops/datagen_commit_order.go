package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
)

const (
	// datagenCommitOrderRows limits the number of newest ledger rows that the
	// commit order report reads for each generated organization.
	datagenCommitOrderRows = 200
	// datagenCommitOrderWindow is how far back the report reads ledger rows.
	datagenCommitOrderWindow = 24 * time.Hour
)

// datagenCommitOrderReport counts the newest node ledger rows of the
// generated organizations. CommitVersions counts the rows with a commit
// version, and Duplicates counts commit versions that more than one row
// stores.
type datagenCommitOrderReport struct {
	Command        string `json:"command"`
	ReadAt         string `json:"read_at"`
	NodeRows       int    `json:"node_rows"`
	CommitVersions int    `json:"commit_versions"`
	Duplicates     int    `json:"duplicates"`
}

// readDatagenCommitOrder uses AUDIT_READER_DSN to read the newest ledger rows
// for each generated organization. It counts node rows, node rows with a
// commit version, and distinct commit versions that appear in multiple node
// rows.
func readDatagenCommitOrder(ctx context.Context, f *cli.Factory, scaleName string, seed int64) (datagenCommitOrderReport, error) {
	now := clock.Now().UTC()
	report := datagenCommitOrderReport{
		Command: "ops.qa.datagen.commit_order", ReadAt: now.Format(time.RFC3339Nano), NodeRows: 0, CommitVersions: 0, Duplicates: 0,
	}
	if strings.TrimSpace(f.Cfg.AuditReaderDSN) == "" {
		err := errors.New("the commit order report needs AUDIT_READER_DSN")
		slog.ErrorContext(ctx, "qa.datagen.commit_order_config_missing", slog.String("err", err.Error()))
		return report, err
	}
	scale, err := datagen.ParseScale(scaleName)
	if err != nil {
		return report, fmt.Errorf("read the commit order: %w", err)
	}
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.commit_order_reader_failed", slog.String("err", err.Error()))
		return report, fmt.Errorf("open the ledger reader: %w", err)
	}
	defer reader.Close()
	seen := map[string]int{}
	read := map[uuid.UUID]bool{}
	for _, workspace := range datagen.PlanIdentities(f.Cfg, seed, scale).Workspaces {
		if read[workspace.OrgID] {
			continue
		}
		read[workspace.OrgID] = true
		page, err := reader.QueryPage(ctx, audit.QueryFilter{
			OrgID: workspace.OrgID, Oldest: now.Add(-datagenCommitOrderWindow), Latest: now.Add(time.Minute),
			Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: datagenCommitOrderRows,
		}, "")
		if err != nil {
			wrapped := fmt.Errorf("read the ledger rows of organization %s: %w", workspace.OrgID, err)
			slog.ErrorContext(ctx, "qa.datagen.commit_order_failed", slog.String("err", wrapped.Error()))
			return report, wrapped
		}
		for _, row := range page.Rows {
			if row.EntityKind != "node" {
				continue
			}
			report.NodeRows++
			if row.Context.CommitVersion == "" {
				continue
			}
			report.CommitVersions++
			seen[row.Context.CommitVersion]++
			if seen[row.Context.CommitVersion] == 2 {
				report.Duplicates++
			}
		}
	}
	return report, nil
}
