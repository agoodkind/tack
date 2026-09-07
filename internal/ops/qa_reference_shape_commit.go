package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/org"
)

// commitReferenceShape writes the corpus and refuses to call the write good
// unless the org then holds what the report says. The org's own post-repair
// deletions are read first, through the ledger reader the reconstruction
// uses, and the corpus is written short by them.
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
	querier, err := newAuditRowQuerier(ctx, env)
	if err != nil {
		return err
	}
	defer querier.Close()

	deletions, err := readReferenceShapeDeletions(ctx, env, querier, shape.OrgID, clock.Now().UTC())
	if err != nil {
		return err
	}
	result.DeletedSubjectsRecorded = len(deletions.Recorded)
	result.DeletedSubjectsUnrecorded = deletions.Unrecorded
	reduced, absent, err := applyReferenceShapeDeletions(shape, deletions)
	if err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.deletions_rejected", slog.String("err", err.Error()))
		return err
	}
	result.Issues = len(reduced.Issues)
	result.Collisions = len(reduced.Groups)
	result.Renames = reduced.Renames

	result.NodesRemoved, err = removeReferenceShapeIssues(ctx, env, shape.OrgID, absent)
	if err != nil {
		return err
	}
	written, err := writeReferenceShape(ctx, env, reduced)
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
