package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
)

// datagenRepairClasses lists the repair classes of the entries that the
// command builds. A dry run prints this list.
var datagenRepairClasses = []string{
	"multiple_child_of_edges", "legacy_status", "select_label", "printed_parent", "absent_parent_uuid", "absent_parent", "test_fixture",
}

type datagenRepairInput struct {
	clispec.InputMarker
	Commit bool
}

type datagenRepairResult struct {
	clispec.ResultMarker
	Command        string                `json:"command"`
	DryRun         bool                  `json:"dry_run"`
	Classes        []string              `json:"classes"`
	OrgID          uuid.UUID             `json:"org_id"`
	ManifestDigest string                `json:"manifest_digest,omitempty"`
	Preview        *NodeIntegrityPreview `json:"preview,omitempty"`
	Applied        *NodeIntegrityApply   `json:"applied,omitempty"`
	Rerun          *NodeIntegrityApply   `json:"rerun,omitempty"`
	Verified       bool                  `json:"verified"`
}

func datagenRepairOp(f *cli.Factory) clispec.Operation[datagenRepairInput] {
	return clispec.Operation[datagenRepairInput]{
		Name:     clispec.Name{Canonical: "repair", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDatagenRepair), Mutates: true},
		Group:    datagenGroup,
		Short:    "Prove the ops backfill once-node-integrity repair path on QA or a local stack.",
		Long: "The command defaults to a dry run and prints the repair classes it seeds. It does not write records during a dry run. " +
			"With --commit, TACK_DATAGEN_ALLOW_TARGET must be exactly qa or local. TACK_REPAIR_DIR must identify the repair directory. " +
			"The command creates a datagen organization, freezes and previews a repair manifest, applies every entry, verifies the results through view and relationship reads, then applies the manifest again. The second application must report every entry as unchanged. " +
			"Each repair ID includes an organization ID suffix. Each run writes new repair checkpoints.",
		Params: []clispec.Param[datagenRepairInput]{
			clispec.BoolParam("commit", "Create the datagen organization and run the repair proof with TACK_DATAGEN_ALLOW_TARGET set to exactly qa or local and the repair directory set by TACK_REPAIR_DIR.", false, func(input *datagenRepairInput, value bool) { input.Commit = value }),
		},
		New: func() datagenRepairInput {
			return datagenRepairInput{InputMarker: clispec.InputMarker{}, Commit: false}
		},
		Run: func(ctx context.Context, input datagenRepairInput, sink clispec.ResultSink) error {
			return runDatagenRepair(ctx, f, input, sink)
		},
	}
}

// runDatagenRepair writes the result, including every step that completed,
// and returns the repair error. When writing the result fails, it returns
// that write failure instead. Without --commit, it writes only the class
// list to the output.
func runDatagenRepair(ctx context.Context, f *cli.Factory, input datagenRepairInput, sink clispec.ResultSink) error {
	result := datagenRepairResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.repair", DryRun: !input.Commit,
		Classes: datagenRepairClasses, OrgID: uuid.Nil, ManifestDigest: "", Preview: nil, Applied: nil, Rerun: nil, Verified: false,
	}
	var runErr error
	if input.Commit {
		runErr = commitDatagenRepair(ctx, f, &result)
	}
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		return datagenRepairFailure(ctx, "write the result", err)
	}
	return runErr
}

func commitDatagenRepair(ctx context.Context, f *cli.Factory, result *datagenRepairResult) error {
	if err := datagen.ValidateTarget(f.Cfg); err != nil {
		return datagenRepairFailure(ctx, "validate the target", err)
	}
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		return datagenRepairFailure(ctx, "read the operator", errors.New("the operator principal is missing"))
	}
	directory, err := openRepairDirectory(ctx, f.Cfg.RepairDir)
	if err != nil {
		return datagenRepairFailure(ctx, "open the repair directory", err)
	}
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		return datagenRepairFailure(ctx, "open the environment", err)
	}
	defer env.Close()
	fixture, err := datagen.SeedRepairFixture(ctx, env.Stores, postgres.NewOrgMemberRepo(env.Pool), principal.ID)
	result.OrgID = fixture.OrgID
	if err != nil {
		return datagenRepairFailure(ctx, "seed the repair fixture", err)
	}
	return runDatagenRepairManifest(ctx, env, fixture, datagenRepairRequest(fixture, directory), result)
}

// runDatagenRepairManifest freezes the request into a manifest, previews it,
// executes it, verifies the results, and executes it again. It stores each
// step result in result and returns an error unless the preview reports
// every entry as planned, the first execution applies every entry, and the
// second execution reports every entry as unchanged. It sets Verified after
// the second execution meets that requirement.
func runDatagenRepairManifest(ctx context.Context, env *Env, fixture datagen.RepairFixture, request NodeIntegrityRequest, result *datagenRepairResult) error {
	repairs := env.Stores.Repairs()
	manifest, err := freezeNodeIntegrityRequest(ctx, repairs, request)
	if err != nil {
		return datagenRepairFailure(ctx, "freeze the manifest", err)
	}
	result.ManifestDigest = manifest.Digest
	preview, err := previewNodeIntegrityManifest(ctx, repairs, manifest)
	result.Preview = &preview
	if err != nil {
		return datagenRepairFailure(ctx, "preview the manifest", err)
	}
	if preview.Counts[RepairPreviewPlanned] != len(manifest.Entries) {
		return datagenRepairFailure(ctx, "preview the manifest", fmt.Errorf("preview counts %v, want %d planned entries", preview.Counts, len(manifest.Entries)))
	}
	applied, err := applyNodeIntegrityManifest(ctx, env, manifest)
	result.Applied = &applied
	if err != nil || applied.Counts[RepairApplyApplied] != len(manifest.Entries) {
		return datagenRepairFailure(ctx, "execute the manifest", errors.Join(fmt.Errorf("apply counts %v, want %d applied entries", applied.Counts, len(manifest.Entries)), err))
	}
	if err := verifyDatagenRepair(ctx, env, fixture, manifest); err != nil {
		return err
	}
	rerun, err := applyNodeIntegrityManifest(ctx, env, manifest)
	result.Rerun = &rerun
	if err != nil || rerun.Counts[RepairApplyUnchanged] != len(manifest.Entries) {
		return datagenRepairFailure(ctx, "execute the manifest again", errors.Join(fmt.Errorf("rerun counts %v, want %d unchanged entries", rerun.Counts, len(manifest.Entries)), err))
	}
	result.Verified = true
	return nil
}

func datagenRepairFailure(ctx context.Context, step string, err error) error {
	wrapped := fmt.Errorf("qa datagen repair: %s: %w", step, err)
	slog.ErrorContext(ctx, "qa.datagen.repair_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
