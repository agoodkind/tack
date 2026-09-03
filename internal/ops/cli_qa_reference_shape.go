package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/org"
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
			"reconstruction can be proven on a testbed. Defaults to a dry run. " +
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
		// A dry run reports what the corpus will derive once it exists; a
		// commit replaces these with what it measures after writing.
		LiveCollisions: len(shape.Groups),
		LiveRenames:    shape.Renames,
		CounterKeys:    len(shape.Projects),
		ReferenceKeys:  len(shape.Issues),
	}
	if !input.Commit {
		return writeReferenceShapeReport(ctx, sink, result)
	}
	return commitReferenceShape(ctx, factory, input, sink, shape, result)
}

func commitReferenceShape(
	ctx context.Context,
	factory *cli.Factory,
	input datagenReferenceShapeInput,
	sink clispec.ResultSink,
	shape referenceShape,
	result datagenReferenceShapeResult,
) error {
	if err := datagen.ValidateTarget(factory.Cfg); err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.target_rejected", slog.String("err", err.Error()))
		return fmt.Errorf("validate the reference shape target: %w", err)
	}
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.env_failed",
			slog.String("err", err.Error()))
		return fmt.Errorf("open the ops environment for the reference shape: %w", err)
	}
	defer env.Close()

	written, err := writeReferenceShape(ctx, env, shape)
	result.NodesCreated = written.Created
	result.NodesRestored = written.Restored
	if err != nil {
		return err
	}
	if err := addReferenceShapeMember(ctx, env, shape.OrgID, input.MemberEmail); err != nil {
		return err
	}
	result.CounterKeys, result.ReferenceKeys, err = measureReferenceShape(ctx, env, shape.OrgID)
	if err != nil {
		return err
	}
	live, err := countLiveReferenceCollisions(ctx, env, shape.OrgID)
	if err != nil {
		return err
	}
	result.LiveCollisions = live.Collisions
	result.LiveRenames = live.Renames
	if err := writeReferenceShapeReport(ctx, sink, result); err != nil {
		return err
	}
	return checkReferenceShape(result)
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

// measureReferenceShape reads back what the reconstruction will derive from
// the corpus, through the same enumeration the reconstruction uses.
func measureReferenceShape(ctx context.Context, env *Env, orgID uuid.UUID) (int, int, error) {
	counters, err := enumerateReferenceCounters(ctx, env, orgID, &referenceRepairStart)
	if err != nil {
		return 0, 0, err
	}
	keys, err := enumerateReferenceKeys(ctx, env, orgID, &referenceRepairStart)
	if err != nil {
		return 0, 0, err
	}
	return len(counters), len(keys), nil
}

// checkReferenceShape refuses to call a commit good unless the org now holds
// what the report describes. The live collision count is the one that matters
// to the repair: a corpus with the right keys and no collisions gives the
// repair nothing to rename, which is the state TACK-475 found.
func checkReferenceShape(result datagenReferenceShapeResult) error {
	if result.CounterKeys != recordedCounterSeeds {
		return fmt.Errorf("the corpus derives %d counter seeds, want %d",
			result.CounterKeys, recordedCounterSeeds)
	}
	want := recordedReferenceKeys + recordedFollowupReferenceKey
	if result.ReferenceKeys != want {
		return fmt.Errorf("the corpus derives %d reference keys, want %d", result.ReferenceKeys, want)
	}
	if result.LiveCollisions != result.Collisions {
		return fmt.Errorf("the org holds %d colliding references after writing, the shape describes %d: "+
			"the repair would find nothing to rename", result.LiveCollisions, result.Collisions)
	}
	if result.LiveRenames != result.Renames {
		return fmt.Errorf("the org holds %d nodes for the repair to rename after writing, the shape describes %d: "+
			"a collision is missing a holder", result.LiveRenames, result.Renames)
	}
	return nil
}

// addReferenceShapeMember grants one existing user membership in the generated
// org. The audit query tools resolve a workspace only through the orgs their
// caller belongs to, so without this the corpus is unreadable through them.
func addReferenceShapeMember(ctx context.Context, env *Env, orgID uuid.UUID, email string) error {
	if email == "" {
		return nil
	}
	stored, err := postgres.NewUserRepo(env.Pool).GetByEmail(ctx, email)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.ErrorContext(ctx, "qa.reference_shape.member_lookup_failed",
			slog.String("err", err.Error()))
		return fmt.Errorf("find the member %q for the reference shape: %w", email, err)
	}
	if stored == nil {
		return fmt.Errorf("no user %q exists to add to the reference shape org", email)
	}
	err = postgres.NewOrgMemberRepo(env.Pool).AddMember(ctx, &org.Member{
		ID: uuid.Nil, OrgID: orgID, UserID: stored.ID,
		Role: referenceShapeMemberRole, CreatedAt: time.Time{},
	})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		slog.ErrorContext(ctx, "qa.reference_shape.member_add_failed",
			slog.String("err", err.Error()))
		return fmt.Errorf("add the member %q to the reference shape org: %w", email, err)
	}
	return nil
}
