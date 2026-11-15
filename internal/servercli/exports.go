package servercli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// auditGroup is the top-level compliance audit ledger family. Every ledger
// read and redaction is an operator command here, never an MCP tool.
var auditGroup = &clispec.Group{
	Use:    "audit",
	Short:  "Compliance audit ledger commands",
	Long:   "",
	Parent: nil,
}

// registerAudit adds the audit family plus hidden back-compat aliases for the
// pre-cobra flat command names (audit-export, audit-verify, gen-audit-key).
func registerAudit(reg *clispec.Registry, factory *cli.Factory) {
	clispec.Register(reg, auditExportOp(factory))
	clispec.Register(reg, auditVerifyOp(factory))
	clispec.Register(reg, auditGenKeyOp(factory))
	clispec.Register(reg, auditSignersOp(factory))
	clispec.Register(reg, auditQueryOp(factory))
	clispec.Register(reg, auditGetOp(factory))
	clispec.Register(reg, auditLastChangeOp(factory))
	clispec.Register(reg, auditRedactActorOp(factory))

	exportAlias := auditExportOp(factory)
	exportAlias.Group, exportAlias.Hidden, exportAlias.Name = nil, true, clispec.Name{
		Canonical:   "audit-export",
		CLIOverride: "",
	}
	clispec.Register(reg, exportAlias)
	verifyAlias := auditVerifyOp(factory)
	verifyAlias.Group, verifyAlias.Hidden, verifyAlias.Name = nil, true, clispec.Name{
		Canonical:   "audit-verify",
		CLIOverride: "",
	}
	clispec.Register(reg, verifyAlias)
	genKeyAlias := auditGenKeyOp(factory)
	genKeyAlias.Group, genKeyAlias.Hidden, genKeyAlias.Name = nil, true, clispec.Name{
		Canonical:   "gen-audit-key",
		CLIOverride: "",
	}
	clispec.Register(reg, genKeyAlias)
}

// auditExportAllRows selects the complete range by default. A row limit would
// omit older evidence from an apparently complete bundle. Streaming bounds
// memory usage independently of the total row count.
const auditExportAllRows = 0

type auditExportInput struct {
	clispec.InputMarker

	Org       string
	Oldest    string
	Latest    string
	Out       string
	RequestID string
	TraceID   string
	Limit     int
}

func auditExportOp(factory *cli.Factory) clispec.Operation[auditExportInput] {
	return clispec.Operation[auditExportInput]{
		DryRun: nil,

		Name:     clispec.Name{Canonical: "export", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit: audit.Spec{
			Atomic:                     false,
			Reads:                      false,
			CreatesAuditInfrastructure: false,
			Class:                      "",
			Targets:                    nil,
			SafeInput:                  nil,
			Verb:                       string(audit.VerbAuditExport),
			Mutates:                    true,
		},
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Export a signed JSONL bundle of audit.events for an org and range",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditExportInput]{
			clispec.StringParam(
				"org",
				"org_id (UUID)",
				"",
				true,
				func(in *auditExportInput, v string) { in.Org = v },
			),
			clispec.StringParam(
				"oldest",
				"RFC3339 lower bound (inclusive)",
				"",
				true,
				func(in *auditExportInput, v string) { in.Oldest = v },
			),
			clispec.StringParam(
				"latest",
				"RFC3339 upper bound (exclusive)",
				"",
				true,
				func(in *auditExportInput, v string) { in.Latest = v },
			),
			clispec.StringParam(
				"out",
				"output directory",
				"",
				true,
				func(in *auditExportInput, v string) { in.Out = v },
			),
			clispec.StringParam(
				"request-id",
				"request_id stored in audit context",
				"",
				false,
				func(in *auditExportInput, v string) { in.RequestID = v },
			),
			clispec.StringParam(
				"trace-id",
				"trace_id stored in audit context",
				"",
				false,
				func(in *auditExportInput, v string) { in.TraceID = v },
			),
			clispec.IntParam(
				"limit",
				"max rows; 0, the default, exports every row in the range",
				auditExportAllRows,
				func(in *auditExportInput, v int) { in.Limit = v },
			),
		},
		New: func() auditExportInput {
			return auditExportInput{
				InputMarker: clispec.InputMarker{},
				Org:         "",
				Oldest:      "",
				Latest:      "",
				Out:         "",
				RequestID:   "",
				TraceID:     "",
				Limit:       auditExportAllRows,
			}
		},
		Run: runAuditExport(factory),
	}
}

func runAuditExport(
	factory *cli.Factory,
) func(context.Context, auditExportInput, clispec.ResultSink) error {
	return func(ctx context.Context, input auditExportInput, sink clispec.ResultSink) error {
		orgID, err := uuid.Parse(strings.TrimSpace(input.Org))
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_bad_org", slog.String("err", err.Error()))

			return fmt.Errorf("audit export: --org must be a UUID: %w", err)
		}

		oldest, err := time.Parse(time.RFC3339, input.Oldest)
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_bad_oldest", slog.String("err", err.Error()))

			return fmt.Errorf("audit export: --oldest must be RFC3339: %w", err)
		}

		latest, err := time.Parse(time.RFC3339, input.Latest)
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_bad_latest", slog.String("err", err.Error()))

			return fmt.Errorf("audit export: --latest must be RFC3339: %w", err)
		}

		reader, err := audit.NewReader(ctx, factory.Cfg.AuditReaderDSN)
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_reader_failed", slog.String("err", err.Error()))

			return fmt.Errorf("audit export: reader from AUDIT_READER_DSN: %w", err)
		}
		defer reader.Close()
		priv, keyID, err := loadAuditKey(factory.Cfg.AuditSigningKeyPath)
		if err != nil {
			return err
		}

		manifest, err := audit.Export(
			ctx,
			reader,
			priv,
			keyID,
			audit.QueryFilter{
				Action:     "",
				ActorID:    uuid.Nil,
				EntityID:   uuid.Nil,
				BeforeTime: time.Time{},
				BeforeSeq:  0,
				NilOrg:     false,

				OrgID:     orgID,
				Oldest:    oldest,
				Latest:    latest,
				RequestID: input.RequestID,
				TraceID:   input.TraceID,
				Limit:     input.Limit,
			},
			input.Out,
		)
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_write_failed", slog.String("err", err.Error()))

			return fmt.Errorf("audit export: write: %w", err)
		}

		return clispec.WriteJSONValue(
			ctx,
			sink,
			auditExportResult{
				ResultMarker: clispec.ResultMarker{},

				ExportID:    manifest.ExportID,
				OrgID:       manifest.OrgID,
				Oldest:      manifest.Oldest,
				Latest:      manifest.Latest,
				RowCount:    manifest.RowCount,
				EventsFile:  manifest.EventsFile,
				FileSHA256:  manifest.FileSHA256,
				SignatureBy: manifest.SignatureBy,
				Signature:   manifest.Signature,
			},
		)
	}
}
