package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// errNoSignerSet is the refusal when neither the flag nor the environment
// names a valid signer.
var errNoSignerSet = errors.New("no valid signer set: set AUDIT_VALID_SIGNERS or pass --signers")

type auditSignersInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Since               string
	Signers             string
	Pub                 string
}

// auditSignersResult is the `audit signers` output: the scan of
// audit.notarizations against the valid signer set.
type auditSignersResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string             `json:"command"`
	Report               audit.SignerReport `json:"report"`
}

// auditSignersOp declares `audit signers`: every notarization since a point
// in time is checked against the valid signer set, and any signed outside it
// is reported by identifier and claimed host (TACK-437).
func auditSignersOp(f *cli.Factory) clispec.Operation[auditSignersInput] {
	return clispec.Operation[auditSignersInput]{
		Name:    clispec.Name{Canonical: "signers", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbAuditSignersVerified), Reads: true},
		Group:   auditGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Check every notarization against the valid signer set and report any signed outside it",
		Long: "Reads audit.notarizations through the reader role. A row whose signing key is " +
			"outside the set fails the command and is reported with its claimed host; a row " +
			"under the local key also has its signature verified. The set comes from " +
			"AUDIT_VALID_SIGNERS or --signers, and the command refuses to run without one.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditSignersInput]{
			clispec.StringParam("since", "RFC3339 lower bound (inclusive); empty scans every row", "", false,
				func(in *auditSignersInput, v string) { in.Since = v }),
			clispec.StringParam("signers", "comma-separated signer identifiers; overrides AUDIT_VALID_SIGNERS", "", false,
				func(in *auditSignersInput, v string) { in.Signers = v }),
			clispec.StringParam("pub", "PEM path to the local ed25519 key (defaults to AUDIT_SIGNING_KEY_PATH)", "", false,
				func(in *auditSignersInput, v string) { in.Pub = v }),
		},
		New: func() auditSignersInput {
			return auditSignersInput{InputMarker: clispec.InputMarker{}, Since: "", Signers: "", Pub: ""}
		},
		Run: runAuditSigners(f),
	}
}

func runAuditSigners(f *cli.Factory) func(context.Context, auditSignersInput, clispec.ResultSink) error {
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
			// Refused before anything is opened: an empty set would accept
			// nothing and a skipped check would accept everything.
			slog.ErrorContext(ctx, "audit.signers_no_set", slog.String("err", errNoSignerSet.Error()))
			return fmt.Errorf("audit signers: %w", errNoSignerSet)
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
		report, err := reader.VerifySigners(ctx, set, localPub, since)
		if err != nil {
			slog.ErrorContext(ctx, "audit.signers_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit signers: %w", err)
		}
		if writeErr := clispec.WriteJSONValue(ctx, sink, auditSignersResult{Command: "audit.signers", Report: *report}); writeErr != nil {
			slog.ErrorContext(ctx, "audit.signers_render_failed", slog.String("err", writeErr.Error()))
			return fmt.Errorf("audit signers: render report: %w", writeErr)
		}
		// The report prints either way; the exit code carries the verdict for
		// a script or a release gate that reads nothing else.
		if verdictErr := report.Err(); verdictErr != nil {
			slog.ErrorContext(ctx, "audit.signers_rejected", slog.String("err", verdictErr.Error()))
			return fmt.Errorf("audit signers: %w", verdictErr)
		}
		return nil
	}
}

// auditSignerSet parses the set from the flag, else from AUDIT_VALID_SIGNERS.
// An empty result is a set with no members, which the callers treat as
// unconfigured.
func auditSignerSet(ctx context.Context, f *cli.Factory, flagValue string) (audit.SignerSet, error) {
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

// auditLocalPublicKey loads the public half of the local signing key from the
// flag, else from AUDIT_SIGNING_KEY_PATH. No key at all is allowed: the set
// check still runs, and every allowed row counts as unverified here.
func auditLocalPublicKey(ctx context.Context, f *cli.Factory, flagValue string) (ed25519.PublicKey, error) {
	keyPath := strings.TrimSpace(flagValue)
	if keyPath == "" {
		keyPath = os.Getenv("AUDIT_SIGNING_KEY_PATH")
	}
	if keyPath == "" && f != nil && f.Cfg != nil {
		keyPath = f.Cfg.AuditSigningKeyPath
	}
	if keyPath == "" {
		return nil, nil
	}
	pub, err := loadAuditPublic(keyPath)
	if err != nil {
		slog.ErrorContext(ctx, "audit.signers_key_failed", slog.String("err", err.Error()))
		return nil, errors.Join(errors.New("audit signers: local key"), err)
	}
	return pub, nil
}
