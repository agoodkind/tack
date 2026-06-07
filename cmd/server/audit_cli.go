package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// auditGroup is the top-level compliance audit bundle family.
var auditGroup = &clispec.Group{Use: "audit", Short: "Compliance audit bundle commands"}

// registerAudit adds the audit family plus hidden back-compat aliases for the
// pre-cobra flat command names (audit-export, audit-verify, gen-audit-key).
func registerAudit(reg *clispec.Registry, f *cli.Factory) {
	clispec.Register(reg, auditExportOp(f))
	clispec.Register(reg, auditVerifyOp(f))
	clispec.Register(reg, auditGenKeyOp(f))

	exportAlias := auditExportOp(f)
	exportAlias.Group, exportAlias.Hidden, exportAlias.Name = nil, true, clispec.Name{Canonical: "audit-export"}
	clispec.Register(reg, exportAlias)
	verifyAlias := auditVerifyOp(f)
	verifyAlias.Group, verifyAlias.Hidden, verifyAlias.Name = nil, true, clispec.Name{Canonical: "audit-verify"}
	clispec.Register(reg, verifyAlias)
	genKeyAlias := auditGenKeyOp(f)
	genKeyAlias.Group, genKeyAlias.Hidden, genKeyAlias.Name = nil, true, clispec.Name{Canonical: "gen-audit-key"}
	clispec.Register(reg, genKeyAlias)
}

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

func auditExportOp(f *cli.Factory) clispec.Operation[auditExportInput] {
	return clispec.Operation[auditExportInput]{
		Name:  clispec.Name{Canonical: "export"},
		Group: auditGroup,
		Short: "Export a signed JSONL bundle of audit.events for an org and range",
		Params: []clispec.Param[auditExportInput]{
			clispec.StringParam("org", "org_id (UUID)", "", true, func(in *auditExportInput, v string) { in.Org = v }),
			clispec.StringParam("oldest", "RFC3339 lower bound (inclusive)", "", true, func(in *auditExportInput, v string) { in.Oldest = v }),
			clispec.StringParam("latest", "RFC3339 upper bound (exclusive)", "", true, func(in *auditExportInput, v string) { in.Latest = v }),
			clispec.StringParam("out", "output directory", "", true, func(in *auditExportInput, v string) { in.Out = v }),
			clispec.StringParam("request-id", "request_id stored in audit context", "", false, func(in *auditExportInput, v string) { in.RequestID = v }),
			clispec.StringParam("trace-id", "trace_id stored in audit context", "", false, func(in *auditExportInput, v string) { in.TraceID = v }),
			clispec.IntParam("limit", "max rows", 100000, func(in *auditExportInput, v int) { in.Limit = v }),
		},
		New: func() auditExportInput { return auditExportInput{Limit: 100000} },
		Run: func(ctx context.Context, in auditExportInput, sink clispec.ResultSink) error {
			orgID, err := uuid.Parse(strings.TrimSpace(in.Org))
			if err != nil {
				return fmt.Errorf("audit export: --org must be a UUID: %w", err)
			}
			oldest, err := time.Parse(time.RFC3339, in.Oldest)
			if err != nil {
				return fmt.Errorf("audit export: --oldest must be RFC3339: %w", err)
			}
			latest, err := time.Parse(time.RFC3339, in.Latest)
			if err != nil {
				return fmt.Errorf("audit export: --latest must be RFC3339: %w", err)
			}
			reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
			if err != nil {
				return fmt.Errorf("audit export: reader: %w", err)
			}
			defer reader.Close()
			priv, keyID, err := loadAuditKey(f.Cfg.AuditSigningKeyPath)
			if err != nil {
				return fmt.Errorf("audit export: key: %w", err)
			}
			manifest, err := audit.Export(ctx, reader, priv, keyID, audit.QueryFilter{
				OrgID: orgID, Oldest: oldest, Latest: latest,
				RequestID: in.RequestID, TraceID: in.TraceID, Limit: in.Limit,
			}, in.Out)
			if err != nil {
				return fmt.Errorf("audit export: write: %w", err)
			}
			return sink.Emit(ctx, manifest)
		},
	}
}

type auditVerifyInput struct {
	clispec.InputMarker
	Bundle string
	Pub    string
}

func auditVerifyOp(f *cli.Factory) clispec.Operation[auditVerifyInput] {
	_ = f
	return clispec.Operation[auditVerifyInput]{
		Name:  clispec.Name{Canonical: "verify"},
		Group: auditGroup,
		Short: "Verify a bundle directory against the audit signing public key",
		Params: []clispec.Param[auditVerifyInput]{
			clispec.StringParam("bundle", "bundle directory produced by audit export", "", true, func(in *auditVerifyInput, v string) { in.Bundle = v }),
			clispec.StringParam("pub", "PEM path to ed25519 public key (defaults to AUDIT_SIGNING_KEY_PATH)", "", false, func(in *auditVerifyInput, v string) { in.Pub = v }),
		},
		New: func() auditVerifyInput { return auditVerifyInput{} },
		Run: func(ctx context.Context, in auditVerifyInput, sink clispec.ResultSink) error {
			keyPath := in.Pub
			if keyPath == "" {
				keyPath = os.Getenv("AUDIT_SIGNING_KEY_PATH")
			}
			if keyPath == "" {
				return fmt.Errorf("audit verify: --pub or AUDIT_SIGNING_KEY_PATH required")
			}
			pub, err := loadAuditPublic(keyPath)
			if err != nil {
				return fmt.Errorf("audit verify: key: %w", err)
			}
			report, err := audit.VerifyBundle(in.Bundle, pub)
			if err != nil {
				return fmt.Errorf("audit verify: scan: %w", err)
			}
			return sink.Emit(ctx, report)
		},
	}
}

type auditGenKeyInput struct {
	clispec.InputMarker
	Output string
}

func auditGenKeyOp(f *cli.Factory) clispec.Operation[auditGenKeyInput] {
	_ = f
	return clispec.Operation[auditGenKeyInput]{
		Name:  clispec.Name{Canonical: "gen-key"},
		Group: auditGroup,
		Short: "Generate an ed25519 audit signing key",
		Args: []clispec.Arg[auditGenKeyInput]{
			clispec.StringArg("output.pem", "destination PEM path", func(in *auditGenKeyInput, v string) { in.Output = v }),
		},
		New: func() auditGenKeyInput { return auditGenKeyInput{} },
		Run: func(ctx context.Context, in auditGenKeyInput, sink clispec.ResultSink) error {
			if err := audit.GenerateAuditSigningKey(in.Output); err != nil {
				return fmt.Errorf("audit gen-key: %w", err)
			}
			return sink.Emit(ctx, map[string]string{"command": "audit.gen_key", "status": "created", "path": in.Output})
		},
	}
}
