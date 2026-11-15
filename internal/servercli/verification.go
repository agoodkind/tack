package servercli

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

var errVerifyKeyRequired = errors.New("audit verify: --pub or AUDIT_SIGNING_KEY_PATH required")

type auditVerifyInput struct {
	clispec.InputMarker

	Bundle  string
	Pub     string
	Signers string
}

// auditVerifyOp declares `audit verify`: an exported bundle is checked
// offline against the signing public key and, when a valid signer set is
// configured, against that set (TACK-437).
func auditVerifyOp(factory *cli.Factory) clispec.Operation[auditVerifyInput] {
	return clispec.Operation[auditVerifyInput]{
		DryRun: nil,

		Name:     clispec.Name{Canonical: "verify", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit: audit.Spec{
			Mutates:                    false,
			Atomic:                     false,
			CreatesAuditInfrastructure: false,
			Class:                      "",
			Targets:                    nil,
			SafeInput:                  nil,
			Verb:                       string(audit.VerbAuditVerify),
			Reads:                      true,
		},
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
		Run: runAuditVerify(factory),
	}
}

func runAuditVerify(
	factory *cli.Factory,
) func(context.Context, auditVerifyInput, clispec.ResultSink) error {
	return func(ctx context.Context, input auditVerifyInput, sink clispec.ResultSink) error {
		keyPath := input.Pub
		if keyPath == "" {
			keyPath = os.Getenv("AUDIT_SIGNING_KEY_PATH")
		}

		if keyPath == "" {
			return errVerifyKeyRequired
		}

		pub, err := loadAuditPublic(keyPath)
		if err != nil {
			return err
		}

		signers, err := auditSignerSet(ctx, factory, input.Signers)
		if err != nil {
			return err
		}

		if !signers.Configured() {
			// Require the deployed signer set. Signature verification alone
			// could accept a key that the environment has revoked.
			slog.ErrorContext(
				ctx,
				"audit.verify_no_signer_set",
				slog.String("bundle", input.Bundle),
				slog.String("err", audit.ErrNoSignerSet.Error()),
			)

			return fmt.Errorf("audit verify: %w", audit.ErrNoSignerSet)
		}

		report, err := audit.VerifyBundleWithSigners(input.Bundle, pub, signers)
		if err != nil {
			slog.ErrorContext(ctx, "audit.verify_failed", slog.String("err", err.Error()))

			return fmt.Errorf("audit verify: scan: %w", err)
		}

		if writeErr := clispec.WriteJSONValue(
			ctx,
			sink,
			auditVerifyResult{
				ResultMarker: clispec.ResultMarker{},

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

		// Print the report before returning a failed verdict. Scripts and
		// release checks also require a failing exit status.
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
