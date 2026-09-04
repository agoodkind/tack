package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/telemetry"
)

type datagenLegacyLedgerRowInput struct {
	clispec.InputMarker
	Commit     bool
	Org        string
	Action     string
	Tool       string
	DeleteNode string
}

type datagenLegacyLedgerRowResult struct {
	clispec.ResultMarker
	Command     string `json:"command"`
	DryRun      bool   `json:"dry_run"`
	OrgID       string `json:"org_id"`
	EventID     string `json:"event_id"`
	Shard       int16  `json:"shard"`
	Seq         int64  `json:"seq"`
	EventTime   string `json:"event_time"`
	Action      string `json:"action,omitempty"`
	Tool        string `json:"tool,omitempty"`
	DeletedNode string `json:"deleted_node,omitempty"`
}

func datagenLegacyLedgerRowOp(f *cli.Factory) clispec.Operation[datagenLegacyLedgerRowInput] {
	return clispec.Operation[datagenLegacyLedgerRowInput]{
		Name:  clispec.Name{Canonical: "legacy-ledger-row", CLIOverride: ""},
		Audit: audit.Spec{Verb: string(audit.VerbOpsDatagenLegacyLedgerRow), Mutates: true},
		Group: datagenGroup,
		Short: "Write one ledger row in the shape that predates the outcome column",
		Long: "Appends a row whose hash version is 1 and whose outcome, error, and " +
			"extra columns are NULL, which is what the writer produced before " +
			"migration 006 added them. Only production holds such rows, so the " +
			"contract that both read tiers report a missing outcome as unrecorded " +
			"cannot otherwise be proven on a testbed. The row joins the real hash " +
			"chain, so verification covers it. Pass --action and --tool to carry a " +
			"different verb, and --delete-node to remove a node first, which " +
			"reproduces production's post-repair deletion whose row names no node. " +
			"Defaults to a dry run.",
		Params: []clispec.Param[datagenLegacyLedgerRowInput]{
			clispec.BoolParam("commit", "write the row after target validation", false,
				func(input *datagenLegacyLedgerRowInput, value bool) { input.Commit = value }),
			clispec.StringParam("org", "org whose chain the row extends (UUID)", "", true,
				func(input *datagenLegacyLedgerRowInput, value string) { input.Org = value }),
			clispec.StringParam("action", "verb the row records; empty writes mcp.tool_invoked", "", false,
				func(input *datagenLegacyLedgerRowInput, value string) { input.Action = value }),
			clispec.StringParam("tool", "MCP tool name recorded in the row context, for example tack_delete_issue", "", false,
				func(input *datagenLegacyLedgerRowInput, value string) { input.Tool = value }),
			clispec.StringParam("delete-node", "node UUID to delete before writing the row", "", false,
				func(input *datagenLegacyLedgerRowInput, value string) { input.DeleteNode = value }),
		},
		New: func() datagenLegacyLedgerRowInput {
			return datagenLegacyLedgerRowInput{
				InputMarker: clispec.InputMarker{}, Commit: false, Org: "",
				Action: "", Tool: "", DeleteNode: "",
			}
		},
		Run: func(
			ctx context.Context,
			input datagenLegacyLedgerRowInput,
			sink clispec.ResultSink,
		) error {
			return runDatagenLegacyLedgerRow(ctx, f, input, sink)
		},
	}
}

func runDatagenLegacyLedgerRow(
	ctx context.Context,
	factory *cli.Factory,
	input datagenLegacyLedgerRowInput,
	sink clispec.ResultSink,
) error {
	orgID, err := uuid.Parse(input.Org)
	if err != nil {
		slog.ErrorContext(ctx, "qa.legacy_ledger_row.bad_org", slog.String("err", err.Error()))
		return fmt.Errorf("legacy ledger row: --org must be a UUID: %w", err)
	}
	deleteNodeID, err := parseOptionalNodeID(ctx, input.DeleteNode)
	if err != nil {
		return err
	}
	result := datagenLegacyLedgerRowResult{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.qa.datagen.legacy-ledger-row",
		DryRun:       !input.Commit,
		OrgID:        orgID.String(),
		EventID:      "", Shard: 0, Seq: 0, EventTime: "",
		Action: input.Action, Tool: input.Tool, DeletedNode: "",
	}
	if !input.Commit {
		return writeLegacyLedgerRowReport(ctx, sink, result)
	}
	if err := datagen.ValidateTarget(factory.Cfg); err != nil {
		slog.ErrorContext(ctx, "qa.legacy_ledger_row.target_rejected", slog.String("err", err.Error()))
		return fmt.Errorf("validate the legacy ledger row target: %w", err)
	}
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		slog.ErrorContext(ctx, "qa.legacy_ledger_row.env_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open the ops environment for the legacy ledger row: %w", err)
	}
	defer env.Close()
	// The node is removed before the row is written, so the ledger's own
	// ordering matches production: the deletion row is the last trace of a node
	// that is already gone.
	if deleteNodeID != uuid.Nil {
		if err := env.Stores.NodeDeleter.DeleteNode(ctx, orgID, deleteNodeID); err != nil {
			slog.ErrorContext(ctx, "qa.legacy_ledger_row.delete_failed",
				slog.String("node_id", deleteNodeID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("delete node %s for the legacy ledger row: %w", deleteNodeID, err)
		}
		result.DeletedNode = deleteNodeID.String()
	}
	// The row is a ledger write, so it goes through the writer role the
	// ledger's grants admit; the application pool holds nothing on the audit
	// schema (TACK-180).
	if factory.Cfg.AuditWriterDSN == "" {
		err := errors.New("legacy ledger row: AUDIT_WRITER_DSN required")
		slog.ErrorContext(ctx, "qa.legacy_ledger_row.writer_dsn_missing", slog.String("err", err.Error()))
		return err
	}
	writer, err := postgres.NewPool(ctx, factory.Cfg.AuditWriterDSN, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "qa.legacy_ledger_row.writer_pool_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open the ledger writer pool for the legacy ledger row: %w", err)
	}
	defer writer.Close()
	row, err := audit.WriteLegacyRow(ctx, writer, audit.LegacyRowInput{
		OrgID: orgID, ActorID: uuid.Must(uuid.NewV7()), EntityID: uuid.Nil,
		EventID: uuid.Nil, Action: input.Action, Tool: input.Tool,
	})
	if err != nil {
		return fmt.Errorf("write the legacy ledger row: %w", err)
	}
	result.EventID = row.EventID.String()
	result.Shard = row.Shard
	result.Seq = row.Seq
	result.EventTime = row.EventTime.Format("2006-01-02T15:04:05.000000Z")
	return writeLegacyLedgerRowReport(ctx, sink, result)
}

// parseOptionalNodeID reads the --delete-node value. Empty means no deletion;
// anything else must be a UUID, so a typo cannot pass as "delete nothing".
func parseOptionalNodeID(ctx context.Context, value string) (uuid.UUID, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return uuid.Nil, nil
	}
	nodeID, err := uuid.Parse(trimmed)
	if err != nil {
		slog.ErrorContext(ctx, "qa.legacy_ledger_row.bad_delete_node", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("legacy ledger row: --delete-node must be a UUID: %w", err)
	}
	return nodeID, nil
}

func writeLegacyLedgerRowReport(
	ctx context.Context,
	sink clispec.ResultSink,
	result datagenLegacyLedgerRowResult,
) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "qa.legacy_ledger_row.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the legacy ledger row report: %w", err)
	}
	return nil
}
