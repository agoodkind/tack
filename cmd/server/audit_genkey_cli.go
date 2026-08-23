package main

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

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
