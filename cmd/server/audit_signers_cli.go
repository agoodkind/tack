package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditSignersInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Since               string
	Signers             string
	Pub                 string
	AllowUnverified     bool
}

// auditSignersResult is the `audit signers` output: the scan of
// audit.notarizations against the valid signer set.
type auditSignersResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string             `json:"command"`
	Report               audit.SignerReport `json:"report"`
}

// auditSignersOp defines audit signers, which checks notarizations since a
// supplied time against the valid signer set and reports rejected signer
// identifiers and their claimed hosts.
func auditSignersOp(f *cli.Factory) clispec.Operation[auditSignersInput] {
	return clispec.Operation[auditSignersInput]{
		Name:     clispec.Name{Canonical: "signers", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbAuditSignersVerified), Reads: true},
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Check every notarization against the valid signer set and report any signed outside it",
		Long: "Run it through the app service, which mounts the signing key and carries the set. " +
			"Reads audit.notarizations through the reader role. A row whose signing key is " +
			"outside the set fails the command and is reported with the hosts it claimed; a row " +
			"under the local key has its signature verified; a row under another allowed key " +
			"cannot be verified here and fails the command unless --allow-unverified is passed. " +
			"The set comes from AUDIT_VALID_SIGNERS or --signers, the key from " +
			"AUDIT_SIGNING_KEY_PATH or --pub, and the command refuses to run without either.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditSignersInput]{
			clispec.StringParam(
				"since",
				"RFC3339 lower bound (inclusive); empty scans every row",
				"",
				false,
				func(in *auditSignersInput, v string) { in.Since = v },
			),
			clispec.StringParam(
				"signers",
				"comma-separated signer identifiers; overrides AUDIT_VALID_SIGNERS",
				"",
				false,
				func(in *auditSignersInput, v string) { in.Signers = v },
			),
			clispec.StringParam(
				"pub",
				"PEM path to the local ed25519 key (defaults to AUDIT_SIGNING_KEY_PATH)",
				"",
				false,
				func(in *auditSignersInput, v string) { in.Pub = v },
			),
			clispec.BoolParam(
				"allow-unverified",
				"accept rows under allowed identifiers this host holds no key for",
				false,
				func(in *auditSignersInput, v bool) { in.AllowUnverified = v },
			),
		},
		New: func() auditSignersInput {
			return auditSignersInput{
				InputMarker:     clispec.InputMarker{},
				Since:           "",
				Signers:         "",
				Pub:             "",
				AllowUnverified: false,
			}
		},
		Run: runAuditSigners(f),
	}
}

func runAuditSigners(
	f *cli.Factory,
) func(context.Context, auditSignersInput, clispec.ResultSink) error {
	return func(ctx context.Context, in auditSignersInput, sink clispec.ResultSink) error {
		since := time.Time{}
		if strings.TrimSpace(in.Since) != "" {
			parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(in.Since))
			if err != nil {
				slog.ErrorContext(ctx, "audit.signers_bad_since", slog.String("err", err.Error()))
				return fmt.Errorf("audit signers: --since must be RFC3339: %w", err)
			}

			since = parsed
		}

		set, err := auditSignerSet(ctx, f, in.Signers)
		if err != nil {
			return err
		}

		if !set.Configured() {
			// The command refuses an unconfigured signer set before opening the key
			// or reader. An empty set does not permit any identifier. VerifySigners
			// also refuses an unconfigured set.
			slog.ErrorContext(
				ctx,
				"audit.signers_no_set",
				slog.String("err", audit.ErrNoSignerSet.Error()),
			)
			return fmt.Errorf("audit signers: %w", audit.ErrNoSignerSet)
		}

		localPub, err := auditLocalPublicKey(ctx, f, in.Pub)
		if err != nil {
			return err
		}

		reader, err := openAuditReader(ctx, f, "audit signers")
		if err != nil {
			return err
		}
		defer reader.Close()
		report, err := reader.VerifySigners(
			ctx,
			audit.SignerCheck{
				Set:                   set,
				LocalPub:              localPub,
				Since:                 since,
				AcknowledgeUnverified: in.AllowUnverified,
			},
		)
		if err != nil {
			slog.ErrorContext(ctx, "audit.signers_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit signers: %w", err)
		}

		if writeErr := clispec.WriteJSONValue(
			ctx,
			sink,
			auditSignersResult{Command: "audit.signers", Report: *report},
		); writeErr != nil {
			slog.ErrorContext(
				ctx,
				"audit.signers_render_failed",
				slog.String("err", writeErr.Error()),
			)

			return fmt.Errorf("audit signers: render report: %w", writeErr)
		}

		// The command prints the report before checking the verdict. Scripts and
		// release checks can use the exit status without reading the report.
		if verdictErr := report.Err(); verdictErr != nil {
			slog.ErrorContext(ctx, "audit.signers_rejected", slog.String("err", verdictErr.Error()))
			return fmt.Errorf("audit signers: %w", verdictErr)
		}

		return nil
	}
}

// auditSignerSet parses the flag value or, when it is empty,
// AUDIT_VALID_SIGNERS. Callers treat an empty set as unconfigured.
func auditSignerSet(
	ctx context.Context,
	f *cli.Factory,
	flagValue string,
) (audit.SignerSet, error) {
	raw := strings.TrimSpace(flagValue)
	if raw == "" && f != nil && f.Cfg != nil {
		raw = f.Cfg.AuditValidSigners
	}

	set, err := audit.ParseSignerSet(raw)
	if err != nil {
		slog.ErrorContext(ctx, "audit.signer_set_invalid", slog.String("err", err.Error()))
		return set, fmt.Errorf("valid signer set: %w", err)
	}

	return set, nil
}

// auditLocalPublicKey loads the local signing public key from the flag or,
// when it is empty, AUDIT_SIGNING_KEY_PATH. It refuses a missing key because
// matching identifier strings alone does not verify a signature.
func auditLocalPublicKey(
	ctx context.Context,
	f *cli.Factory,
	flagValue string,
) (ed25519.PublicKey, error) {
	keyPath := strings.TrimSpace(flagValue)
	if keyPath == "" && f != nil && f.Cfg != nil {
		keyPath = f.Cfg.AuditSigningKeyPath
	}

	if keyPath == "" {
		slog.ErrorContext(
			ctx,
			"audit.signers_no_key",
			slog.String("err", audit.ErrNoLocalKey.Error()),
		)

		return nil, fmt.Errorf("audit signers: %w", audit.ErrNoLocalKey)
	}

	pub, err := loadAuditPublic(keyPath)
	if err != nil {
		slog.ErrorContext(ctx, "audit.signers_key_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit signers: local key: %w", err)
	}

	return pub, nil
}
