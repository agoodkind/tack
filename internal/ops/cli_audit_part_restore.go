package ops

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditPartRestoreFlags struct {
	Manifest       string
	ManifestSHA256 string
}

type auditPartRestoreInput struct {
	ManifestFileSHA256 string    `json:"manifest_file_sha256"`
	OrgID              uuid.UUID `json:"org_id"`
	TargetEventID      uuid.UUID `json:"target_event_id"`
	SourceEventID      uuid.UUID `json:"source_event_id"`
	PartIndex          int64     `json:"part_index"`
	RootDigest         string    `json:"root_digest"`
}

func (auditPartRestoreInput) isCommandInput() {}

func auditPartRestoreSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbOpsBackfillRestoreManifestPart), Mutates: true, Class: audit.OperationExternal,
		Targets: func(ctx context.Context, input json.RawMessage) ([]audit.Target, error) {
			var decoded auditPartRestoreInput
			if err := json.Unmarshal(input, &decoded); err != nil {
				return nil, partRestoreFailure(ctx, "decode part restore input", err)
			}
			return []audit.Target{auditPartTarget(decoded.TargetEventID, decoded.PartIndex)}, nil
		},
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
}

func auditPartRestoreCobra(f *cli.Factory) *cobra.Command {
	flags := auditPartRestoreFlags{Manifest: "", ManifestSHA256: ""}
	cmd := &cobra.Command{
		Use:   "restore-manifest-part",
		Short: "Restore the one missing read manifest part of an audit event from a verified source event",
		Long: "Reads the frozen manifest file inside TACK_REPAIR_DIR, checks its SHA-256 against --manifest-sha256, " +
			"verifies the source event's manifest payload, and checks the pinned row hash, seq, and shard of both events. " +
			"A dry run prints the plan. With --execute, the command calls audit.restore_missing_manifest_part, which " +
			"inserts the part and writes the change event in one transaction. A rerun reports already_present. " +
			"Run it through the audit-ops service.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&flags.Manifest, "manifest", "", "Absolute path of the frozen manifest file inside TACK_REPAIR_DIR.")
	cmd.Flags().StringVar(&flags.ManifestSHA256, "manifest-sha256", "", "SHA-256 of the manifest file.")
	_ = cmd.MarkFlagRequired("manifest")
	_ = cmd.MarkFlagRequired("manifest-sha256")
	input := func() (json.RawMessage, error) {
		manifest, fileDigest, err := readAuditPartRestoreManifest(cmd.Context(), f.Cfg.RepairDir, flags.Manifest, flags.ManifestSHA256)
		if err != nil {
			return nil, err
		}
		return encodeCommandInput(auditPartRestoreInput{
			ManifestFileSHA256: fileDigest, OrgID: manifest.OrgID, TargetEventID: manifest.TargetEventID,
			SourceEventID: manifest.SourceEventID, PartIndex: manifest.PartIndex, RootDigest: manifest.ManifestSHA256,
		})
	}
	spec := auditPartRestoreSpec()
	clispec.AttachExternalPreview(cmd, f, spec, input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runAuditPartRestore(ctx, f, flags, clispec.NewCLISink(f), steps)
		},
		recordedSearchPreview(f, spec, input, func(ctx context.Context, sink clispec.ResultSink) error {
			return previewAuditPartRestore(ctx, f, flags, sink)
		}))
	return cmd
}
