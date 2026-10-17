package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// nodeIntegrityBackfillTool is the tool name in node integrity backfill
// ledger events.
const nodeIntegrityBackfillTool = "ops.backfill.once-node-integrity"

type nodeIntegrityBackfillInput struct {
	clispec.InputMarker
	Request  string
	Manifest string
}

type nodeIntegrityBackfillOutput struct {
	clispec.ResultMarker
	Command  string                 `json:"command"`
	DryRun   bool                   `json:"dry_run"`
	Manifest *NodeIntegrityManifest `json:"manifest,omitempty"`
	Preview  *NodeIntegrityPreview  `json:"preview,omitempty"`
	Applied  *NodeIntegrityApply    `json:"applied,omitempty"`
}

func nodeIntegrityBackfillOp(f *cli.Factory) clispec.Operation[nodeIntegrityBackfillInput] {
	return clispec.Operation[nodeIntegrityBackfillInput]{
		Name:     clispec.Name{Canonical: "node-integrity", CLIOverride: ""},
		Lifetime: clispec.Lifetime{Ticket: "TACK-582", RemoveBy: time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC)},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsBackfillNodeIntegrity), Mutates: true},
		Group:    opsGroup,
		Short:    "This command freezes, previews, and applies reviewed node integrity repairs.",
		Long: "The command requires exactly one of --request or --manifest. " +
			"A dry run with --request reads current records for each reviewed entry and prints a frozen manifest with its digest. " +
			"A dry run with --manifest validates the manifest and prints each entry's state against current records and checkpoints. " +
			"With --execute and --manifest, the command applies each executable entry phase by phase. " +
			"The command skips an entry when a current record differs from the frozen manifest. " +
			"The command rejects --request with --execute.",
		Params: []clispec.Param[nodeIntegrityBackfillInput]{
			clispec.StringParam("request", "This path selects the reviewed repair request for a dry run that freezes a manifest.", "", false,
				func(in *nodeIntegrityBackfillInput, value string) { in.Request = value }),
			clispec.StringParam("manifest", "This path selects the frozen repair manifest for preview or execution.", "", false,
				func(in *nodeIntegrityBackfillInput, value string) { in.Manifest = value }),
		},
		New: func() nodeIntegrityBackfillInput {
			return nodeIntegrityBackfillInput{InputMarker: clispec.InputMarker{}, Request: "", Manifest: ""}
		},
		DryRun: func(ctx context.Context, in nodeIntegrityBackfillInput, sink clispec.ResultSink) error {
			return runNodeIntegrityBackfillCommand(ctx, f, in, sink, true)
		},
		Run: func(ctx context.Context, in nodeIntegrityBackfillInput, sink clispec.ResultSink) error {
			return runNodeIntegrityBackfillCommand(ctx, f, in, sink, false)
		},
	}
}

func runNodeIntegrityBackfillCommand(ctx context.Context, f *cli.Factory, in nodeIntegrityBackfillInput, sink clispec.ResultSink, dryRun bool) error {
	request, manifestPath := strings.TrimSpace(in.Request), strings.TrimSpace(in.Manifest)
	if (request == "") == (manifestPath == "") {
		return nodeIntegrityCommandFailure(ctx, "read the input", errors.New("pass exactly one of --request and --manifest"))
	}
	if request != "" && !dryRun {
		return nodeIntegrityCommandFailure(ctx, "read the input",
			errors.New("--request only freezes a manifest; review the frozen manifest and execute it with --manifest"))
	}
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		return nodeIntegrityCommandFailure(ctx, "open the environment", err)
	}
	defer env.Close()
	repairs := env.Stores.Repairs()
	output := nodeIntegrityBackfillOutput{
		ResultMarker: clispec.ResultMarker{}, Command: nodeIntegrityBackfillTool, DryRun: dryRun,
		Manifest: nil, Preview: nil, Applied: nil,
	}
	switch {
	case request != "":
		var reviewed NodeIntegrityRequest
		if err := readNodeIntegrityDocument(ctx, request, &reviewed); err != nil {
			return err
		}
		frozen, err := freezeNodeIntegrityRequest(ctx, repairs, reviewed)
		if err != nil {
			return err
		}
		output.Manifest = &frozen
	default:
		var manifest NodeIntegrityManifest
		if err := readNodeIntegrityDocument(ctx, manifestPath, &manifest); err != nil {
			return err
		}
		if err := checkNodeIntegrityManifest(ctx, manifest); err != nil {
			return repairManifestRejected(ctx, "validate", err)
		}
		if dryRun {
			preview, err := previewNodeIntegrityManifest(ctx, repairs, manifest)
			if err != nil {
				return err
			}
			output.Preview = &preview
			break
		}
		applied, applyErr := applyNodeIntegrityManifest(ctx, env, manifest)
		output.Applied = &applied
		if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
			return nodeIntegrityCommandFailure(ctx, "write the result", err)
		}
		return applyErr
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		return nodeIntegrityCommandFailure(ctx, "write the result", err)
	}
	return nil
}

func nodeIntegrityCommandFailure(ctx context.Context, step string, err error) error {
	wrapped := fmt.Errorf("node integrity backfill: %s: %w", step, err)
	slog.ErrorContext(ctx, "repair.command_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
