package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// referenceShapeMemberRole is the org role the named member receives, the same
// role the product seed grants its first user, so the audit query tools reach
// the generated org through the ordinary membership check.
const referenceShapeMemberRole = 20

type datagenReferenceShapeInput struct {
	clispec.InputMarker
	Commit      bool
	MemberEmail string
}

type datagenReferenceShapeResult struct {
	clispec.ResultMarker
	Command       string `json:"command"`
	DryRun        bool   `json:"dry_run"`
	OrgSlug       string `json:"org_slug"`
	OrgID         string `json:"org_id"`
	Scopes        int    `json:"scopes"`
	Issues        int    `json:"issues"`
	Collisions    int    `json:"collisions"`
	Renames       int    `json:"expected_renames"`
	NodesCreated  int    `json:"nodes_created"`
	NodesRestored int    `json:"nodes_restored"`
	// NodesRemoved counts nodes the ledger records as deleted that an earlier
	// run had put back; a commit removes them so the store agrees with the
	// ledger the reconstruction reads (TACK-473).
	NodesRemoved int `json:"nodes_removed"`
	// DeletedSubjectsRecorded and DeletedSubjectsUnrecorded are the post-repair
	// deletions the org's ledger holds, by whether the delete row names its
	// node. The corpus is written short by their sum, because the
	// reconstruction counts them on top of what is present.
	DeletedSubjectsRecorded   int `json:"deleted_subjects_recorded"`
	DeletedSubjectsUnrecorded int `json:"deleted_subjects_unrecorded"`
	// LiveCollisions is counted from the org after writing, through the
	// repair's own duplicate scan. Collisions above is what the shape
	// describes; this is what exists. A commit that leaves them unequal
	// fails, because a generator that reports collisions it did not create
	// leaves the repair nothing to do while claiming otherwise (TACK-475).
	LiveCollisions int `json:"live_collisions"`
	// LiveRenames is the number of nodes the repair will move, counted the
	// same way. It catches a corpus with the right number of groups but a
	// holder missing from one, which the group count alone would pass.
	LiveRenames   int `json:"live_renames"`
	CounterKeys   int `json:"counter_keys_before_repair"`
	ReferenceKeys int `json:"reference_keys_before_repair"`
}

func datagenReferenceShapeOp(f *cli.Factory) clispec.Operation[datagenReferenceShapeInput] {
	return clispec.Operation[datagenReferenceShapeInput]{
		Name:  clispec.Name{Canonical: "reference-repair-shape", CLIOverride: ""},
		Audit: audit.Spec{Verb: string(audit.VerbOpsDatagenReferenceRepairShape), Mutates: true},
		Group: datagenGroup,
		Short: "Write the corpus the 2026-08-07 reference repair ran against",
		Long: "Recreates the colliding references, the scopes that held them, and " +
			"the issues the repair keyed, so the repair and its ledger " +
			"reconstruction can be proven on a testbed. Nodes the org's ledger " +
			"records as deleted after the repair stay absent, so the " +
			"reconstruction derives the recorded count on an org that has been " +
			"deleted from. Defaults to a dry run. " +
			"Pass --commit only where FoundationDB is reachable, which is the " +
			"app container rather than the host-networked ops container.",
		Params: []clispec.Param[datagenReferenceShapeInput]{
			clispec.BoolParam("commit", "write the corpus after target validation", false,
				func(input *datagenReferenceShapeInput, value bool) { input.Commit = value }),
			clispec.StringParam("member-email", "grant this existing user membership in the generated org", "", false,
				func(input *datagenReferenceShapeInput, value string) { input.MemberEmail = value }),
		},
		New: func() datagenReferenceShapeInput {
			return datagenReferenceShapeInput{
				InputMarker: clispec.InputMarker{}, Commit: false, MemberEmail: "",
			}
		},
		Run: func(
			ctx context.Context,
			input datagenReferenceShapeInput,
			sink clispec.ResultSink,
		) error {
			return runDatagenReferenceShape(ctx, f, input, sink)
		},
	}
}

func runDatagenReferenceShape(
	ctx context.Context,
	factory *cli.Factory,
	input datagenReferenceShapeInput,
	sink clispec.ResultSink,
) error {
	renames, err := loadReferenceRenameEvidence(ctx)
	if err != nil {
		return err
	}
	shape, err := deriveReferenceShape(renames)
	if err != nil {
		return err
	}
	result := datagenReferenceShapeResult{
		ResultMarker:  clispec.ResultMarker{},
		Command:       "ops.qa.datagen.reference-repair-shape",
		DryRun:        !input.Commit,
		OrgSlug:       productionSeedOrgSlug,
		OrgID:         shape.OrgID.String(),
		Scopes:        len(shape.Projects),
		Issues:        len(shape.Issues),
		Collisions:    len(shape.Groups),
		Renames:       shape.Renames,
		NodesCreated:  0,
		NodesRestored: 0,
		NodesRemoved:  0,
		// A dry run cannot read the org's deletions without the stores, so it
		// reports the full shape; a commit replaces these with what the ledger
		// holds and what it measures after writing.
		DeletedSubjectsRecorded:   0,
		DeletedSubjectsUnrecorded: 0,
		LiveCollisions:            len(shape.Groups),
		LiveRenames:               shape.Renames,
		CounterKeys:               len(shape.Projects),
		ReferenceKeys:             len(shape.Issues),
	}
	if !input.Commit {
		return writeReferenceShapeReport(ctx, sink, result)
	}
	return commitReferenceShape(ctx, factory, input, sink, shape, result)
}

func writeReferenceShapeReport(
	ctx context.Context,
	sink clispec.ResultSink,
	result datagenReferenceShapeResult,
) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the reference shape report: %w", err)
	}
	return nil
}
