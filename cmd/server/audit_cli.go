package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// auditGroup is the top-level compliance audit bundle family.
var auditGroup = &clispec.Group{Use: "audit", Short: "Compliance audit bundle commands", Long: "", Parent: nil}

// registerAudit adds the audit family plus hidden back-compat aliases for the
// pre-cobra flat command names (audit-export, audit-verify, gen-audit-key).
func registerAudit(reg *clispec.Registry, f *cli.Factory) {
	clispec.Register(reg, auditExportOp(f))
	clispec.Register(reg, auditVerifyOp(f))
	clispec.Register(reg, auditGenKeyOp(f))

	exportAlias := auditExportOp(f)
	exportAlias.Group, exportAlias.Hidden, exportAlias.Name = nil, true, clispec.Name{Canonical: "audit-export", CLIOverride: ""}
	clispec.Register(reg, exportAlias)
	verifyAlias := auditVerifyOp(f)
	verifyAlias.Group, verifyAlias.Hidden, verifyAlias.Name = nil, true, clispec.Name{Canonical: "audit-verify", CLIOverride: ""}
	clispec.Register(reg, verifyAlias)
	genKeyAlias := auditGenKeyOp(f)
	genKeyAlias.Group, genKeyAlias.Hidden, genKeyAlias.Name = nil, true, clispec.Name{Canonical: "gen-audit-key", CLIOverride: ""}
	clispec.Register(reg, genKeyAlias)
}

type auditExportInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Org                 string
	Oldest              string
	Latest              string
	Out                 string
	RequestID           string
	TraceID             string
	Limit               int
}

func auditExportOp(f *cli.Factory) clispec.Operation[auditExportInput] {
	return clispec.Operation[auditExportInput]{
		Name:     clispec.Name{Canonical: "export", CLIOverride: ""},
		Audit:    audit.Spec{Verb: string(audit.VerbAuditExport), Mutates: true},
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Export a signed JSONL bundle of audit.events for an org and range",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditExportInput]{
			clispec.StringParam("org", "org_id (UUID)", "", true, func(in *auditExportInput, v string) { in.Org = v }),
			clispec.StringParam("oldest", "RFC3339 lower bound (inclusive)", "", true, func(in *auditExportInput, v string) { in.Oldest = v }),
			clispec.StringParam("latest", "RFC3339 upper bound (exclusive)", "", true, func(in *auditExportInput, v string) { in.Latest = v }),
			clispec.StringParam("out", "output directory", "", true, func(in *auditExportInput, v string) { in.Out = v }),
			clispec.StringParam("request-id", "request_id stored in audit context", "", false, func(in *auditExportInput, v string) { in.RequestID = v }),
			clispec.StringParam("trace-id", "trace_id stored in audit context", "", false, func(in *auditExportInput, v string) { in.TraceID = v }),
			clispec.IntParam("limit", "max rows", 100000, func(in *auditExportInput, v int) { in.Limit = v }),
		},
		New: func() auditExportInput {
			return auditExportInput{InputMarker: clispec.InputMarker{}, Org: "", Oldest: "", Latest: "", Out: "", RequestID: "", TraceID: "", Limit: 100000}
		},
		Run: runAuditExport(f),
	}
}

func runAuditExport(f *cli.Factory) func(context.Context, auditExportInput, clispec.ResultSink) error {
	return func(ctx context.Context, in auditExportInput, sink clispec.ResultSink) error {
		orgID, err := uuid.Parse(strings.TrimSpace(in.Org))
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_bad_org", slog.String("err", err.Error()))
			return fmt.Errorf("audit export: --org must be a UUID: %w", err)
		}
		oldest, err := time.Parse(time.RFC3339, in.Oldest)
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_bad_oldest", slog.String("err", err.Error()))
			return fmt.Errorf("audit export: --oldest must be RFC3339: %w", err)
		}
		latest, err := time.Parse(time.RFC3339, in.Latest)
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_bad_latest", slog.String("err", err.Error()))
			return fmt.Errorf("audit export: --latest must be RFC3339: %w", err)
		}
		reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_reader_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit export: reader: %w", err)
		}
		defer reader.Close()
		priv, keyID, err := loadAuditKey(f.Cfg.AuditSigningKeyPath)
		if err != nil {
			return err
		}
		manifest, err := audit.Export(ctx, reader, priv, keyID, audit.QueryFilter{
			OrgID: orgID, Oldest: oldest, Latest: latest,
			RequestID: in.RequestID, TraceID: in.TraceID, Limit: in.Limit,
		}, in.Out)
		if err != nil {
			slog.ErrorContext(ctx, "audit.export_write_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit export: write: %w", err)
		}
		return clispec.WriteJSONValue(ctx, sink, auditExportResult{
			ExportID: manifest.ExportID, OrgID: manifest.OrgID, Oldest: manifest.Oldest,
			Latest: manifest.Latest, RowCount: manifest.RowCount, FileSHA256: manifest.FileSHA256,
			SignatureBy: manifest.SignatureBy, Signature: manifest.Signature,
		})
	}
}

type auditVerifyInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Bundle              string
	Pub                 string
}

func auditVerifyOp(f *cli.Factory) clispec.Operation[auditVerifyInput] {
	_ = f
	return clispec.Operation[auditVerifyInput]{
		Name:     clispec.Name{Canonical: "verify", CLIOverride: ""},
		Audit:    audit.Spec{Verb: string(audit.VerbAuditVerify), Reads: true},
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Verify a bundle directory against the audit signing public key",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditVerifyInput]{
			clispec.StringParam("bundle", "bundle directory produced by audit export", "", true, func(in *auditVerifyInput, v string) { in.Bundle = v }),
			clispec.StringParam("pub", "PEM path to ed25519 public key (defaults to AUDIT_SIGNING_KEY_PATH)", "", false, func(in *auditVerifyInput, v string) { in.Pub = v }),
		},
		New: func() auditVerifyInput {
			return auditVerifyInput{InputMarker: clispec.InputMarker{}, Bundle: "", Pub: ""}
		},
		Run: func(ctx context.Context, in auditVerifyInput, sink clispec.ResultSink) error {
			keyPath := in.Pub
			if keyPath == "" {
				keyPath = os.Getenv("AUDIT_SIGNING_KEY_PATH")
			}
			if keyPath == "" {
				return errors.New("audit verify: --pub or AUDIT_SIGNING_KEY_PATH required")
			}
			pub, err := loadAuditPublic(keyPath)
			if err != nil {
				return err
			}
			report, err := audit.VerifyBundle(in.Bundle, pub)
			if err != nil {
				slog.ErrorContext(ctx, "audit.verify_failed", slog.String("err", err.Error()))
				return fmt.Errorf("audit verify: scan: %w", err)
			}
			if writeErr := clispec.WriteJSONValue(ctx, sink, auditVerifyResult{
				BundleDir: report.BundleDir, RowsScanned: report.RowsScanned, HashMatches: report.HashMatches,
				LinkageOnlyRows: report.LinkageOnlyRows,
				ChainGapCount:   report.ChainGapCount, ChainBreaks: report.ChainBreaks, FileSHA256OK: report.FileSHA256OK,
				SignatureOK: report.SignatureOK, ManifestSubject: report.ManifestSubject,
			}); writeErr != nil {
				slog.ErrorContext(ctx, "audit.verify_render_failed", slog.String("err", writeErr.Error()))
				return fmt.Errorf("audit verify: render report: %w", writeErr)
			}
			// The report prints either way, so the human path is unchanged.
			// The exit code has to carry the same verdict, because a script or
			// a release gate reads only that.
			if verdictErr := report.Err(); verdictErr != nil {
				slog.ErrorContext(ctx, "audit.verify_rejected",
					slog.String("bundle", report.BundleDir),
					slog.String("err", verdictErr.Error()))
				return fmt.Errorf("audit verify: %w", verdictErr)
			}
			return nil
		},
	}
}

type auditGenKeyInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Output              string
}

func auditGenKeyOp(f *cli.Factory) clispec.Operation[auditGenKeyInput] {
	_ = f
	return clispec.Operation[auditGenKeyInput]{
		Name:     clispec.Name{Canonical: "gen-key", CLIOverride: ""},
		Audit:    audit.Spec{Verb: string(audit.VerbAuditKeyGenerate), Mutates: true},
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Generate an ed25519 audit signing key",
		Long:     "",
		Examples: nil,
		Args: []clispec.Arg[auditGenKeyInput]{
			clispec.StringArg("output.pem", "destination PEM path", func(in *auditGenKeyInput, v string) { in.Output = v }),
		},
		Params: nil,
		New:    func() auditGenKeyInput { return auditGenKeyInput{InputMarker: clispec.InputMarker{}, Output: ""} },
		Run: func(ctx context.Context, in auditGenKeyInput, sink clispec.ResultSink) error {
			if err := audit.GenerateAuditSigningKey(in.Output); err != nil {
				slog.ErrorContext(ctx, "audit.gen_key_failed", slog.String("err", err.Error()))
				return fmt.Errorf("audit gen-key: %w", err)
			}
			return clispec.WriteJSONValue(ctx, sink, auditGenKeyResult{
				Command: "audit.gen_key", Status: "created", Path: in.Output,
			})
		},
	}
}
