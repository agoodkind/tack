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
	Manifest       string `json:"manifest"`
	ManifestSHA256 string `json:"manifest_sha256"`
	TargetEventID  string `json:"target_event_id"`
}

func (auditPartRestoreFlags) isCommandInput() {}

func auditPartRestoreSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbOpsBackfillRestoreManifestPart), Mutates: true, Class: audit.OperationExternal,
		Targets: func(ctx context.Context, input json.RawMessage) ([]audit.Target, error) {
			var decoded auditPartRestoreFlags
			if err := json.Unmarshal(input, &decoded); err != nil {
				return nil, partRestoreFailure(ctx, "decode part restore input", err)
			}
			return []audit.Target{auditPartRestoreOperationTarget(decoded.TargetEventID)}, nil
		},
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
}

func auditPartRestoreOperationTarget(targetFlag string) audit.Target {
	targetID, err := uuid.Parse(targetFlag)
	if err != nil {
		return auditResourceTarget(auditEventInvalidURI)
	}
	return auditPartTarget(targetID, 0)
}

func auditPartRestoreCobra(f *cli.Factory) *cobra.Command {
	flags := auditPartRestoreFlags{Manifest: "", ManifestSHA256: "", TargetEventID: ""}
	cmd := &cobra.Command{
		Use:   "restore-manifest-part",
		Short: "Restore a missing audit event read manifest part from a verified source event",
		Long: "Run this command through the audit-ops service. It reads the frozen manifest file and checks its " +
			"SHA-256 against --manifest-sha256. It verifies the source event's manifest payload and checks the " +
			"pinned row hash, sequence, and shard of both events. Without --execute, it prints the plan and does " +
			"not change anything. With --execute, it copies the verified part and records the change in one " +
			"transaction. The transaction saves both changes or neither change. A rerun reports already_present.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&flags.Manifest, "manifest", "", "Set --manifest to the absolute path of the frozen manifest file inside TACK_REPAIR_DIR.")
	cmd.Flags().StringVar(&flags.ManifestSHA256, "manifest-sha256", "", "Set --manifest-sha256 to the SHA-256 of the manifest file.")
	cmd.Flags().StringVar(&flags.TargetEventID, "target-event-id", "", "Set --target-event-id to the ID of the audit event that is missing the manifest part the command restores.")
	_ = cmd.MarkFlagRequired("manifest")
	_ = cmd.MarkFlagRequired("manifest-sha256")
	_ = cmd.MarkFlagRequired("target-event-id")
	var request *auditPartRestoreRequest
	cmd.PreRunE = func(*cobra.Command, []string) error {
		request = nil
		return nil
	}
	load := func(ctx context.Context) (auditPartRestoreRequest, error) {
		if request != nil {
			return *request, nil
		}
		targetID, err := uuid.Parse(flags.TargetEventID)
		if err != nil {
			return auditPartRestoreRequest{}, manifestRefused(ctx, "--target-event-id is not a UUID")
		}
		manifest, fileDigest, err := readAuditPartRestoreManifest(ctx, f.Cfg.RepairDir, flags.Manifest, flags.ManifestSHA256)
		if err != nil {
			return auditPartRestoreRequest{}, err
		}
		if manifest.TargetEventID != targetID {
			return auditPartRestoreRequest{}, manifestRefused(ctx, "manifest target_event_id differs from --target-event-id")
		}
		request = &auditPartRestoreRequest{manifest: manifest, fileDigest: fileDigest}
		return *request, nil
	}
	input := func() (json.RawMessage, error) { return encodeCommandInput(flags) }
	spec := auditPartRestoreSpec()
	clispec.AttachExternalPreview(cmd, f, spec, input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			loaded, err := load(ctx)
			if err != nil {
				return outcomeFor(err), err
			}
			return runAuditPartRestore(ctx, f, loaded, clispec.NewCLISink(f), steps)
		},
		recordedSearchPreview(f, spec, input, func(ctx context.Context, sink clispec.ResultSink) error {
			loaded, err := load(ctx)
			if err != nil {
				return previewFailure(err)
			}
			if err := previewAuditPartRestore(ctx, f, loaded, sink); err != nil {
				return previewFailure(err)
			}
			return nil
		}))
	return cmd
}
