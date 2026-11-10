package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditVerifyInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Bundle              string
	Pub                 string
	Signers             string
}

// auditVerifyOp defines audit verify, which checks an exported bundle
// offline against the signing public key and a required valid signer set.
func auditVerifyOp(f *cli.Factory) clispec.Operation[auditVerifyInput] {
	return clispec.Operation[auditVerifyInput]{
		Name:     clispec.Name{Canonical: "verify", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbAuditVerify), Reads: true},
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Verify a bundle directory against the audit signing public key and the valid signer set",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditVerifyInput]{
			clispec.StringParam(
				"bundle",
				"bundle directory produced by audit export",
				"",
				true,
				func(in *auditVerifyInput, v string) { in.Bundle = v },
			),
			clispec.StringParam(
				"pub",
				"PEM path to ed25519 public key (defaults to AUDIT_SIGNING_KEY_PATH)",
				"",
				false,
				func(in *auditVerifyInput, v string) { in.Pub = v },
			),
			clispec.StringParam(
				"signers",
				"comma-separated valid signer identifiers; overrides AUDIT_VALID_SIGNERS",
				"",
				false,
				func(in *auditVerifyInput, v string) { in.Signers = v },
			),
		},
		New: func() auditVerifyInput {
			return auditVerifyInput{
				InputMarker: clispec.InputMarker{},
				Bundle:      "",
				Pub:         "",
				Signers:     "",
			}
		},
		Run: runAuditVerify(f),
	}
}

func runAuditVerify(
	f *cli.Factory,
) func(context.Context, auditVerifyInput, clispec.ResultSink) error {
	return func(ctx context.Context, in auditVerifyInput, sink clispec.ResultSink) error {
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

		signers, err := auditSignerSet(ctx, f, in.Signers)
		if err != nil {
			return err
		}

		if !signers.Configured() {
			// The command requires a valid signer set. A valid signature alone does
			// not establish that the environment still allows the signing key.
			slog.ErrorContext(
				ctx,
				"audit.verify_no_signer_set",
				slog.String("bundle", in.Bundle),
				slog.String("err", audit.ErrNoSignerSet.Error()),
			)

			return fmt.Errorf("audit verify: %w", audit.ErrNoSignerSet)
		}

		report, err := audit.VerifyBundleWithSigners(in.Bundle, pub, signers)
		if err != nil {
			slog.ErrorContext(ctx, "audit.verify_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit verify: scan: %w", err)
		}

		if writeErr := clispec.WriteJSONValue(
			ctx,
			sink,
			auditVerifyResult{
				BundleDir:           report.BundleDir,
				RowsScanned:         report.RowsScanned,
				HashMatches:         report.HashMatches,
				ChainGapCount:       report.ChainGapCount,
				ChainBreaks:         report.ChainBreaks,
				FileSHA256OK:        report.FileSHA256OK,
				SignatureOK:         report.SignatureOK,
				ManifestSubject:     report.ManifestSubject,
				ManifestSigner:      report.ManifestSigner,
				VerifiedSigner:      report.VerifiedSigner,
				SignerSetConfigured: report.SignerSetConfigured,
				SignerAllowed:       report.SignerAllowed,
			},
		); writeErr != nil {
			slog.ErrorContext(
				ctx,
				"audit.verify_render_failed",
				slog.String("err", writeErr.Error()),
			)

			return fmt.Errorf("audit verify: render report: %w", writeErr)
		}

		// The command prints the report before checking the verdict. Scripts and
		// release checks can use the exit status without reading the report.
		if verdictErr := report.Err(); verdictErr != nil {
			slog.ErrorContext(
				ctx,
				"audit.verify_rejected",
				slog.String("bundle", report.BundleDir),
				slog.String("err", verdictErr.Error()),
			)

			return fmt.Errorf("audit verify: %w", verdictErr)
		}

		return nil
	}
}
