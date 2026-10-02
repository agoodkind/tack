package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/org"
)

// verifyReplay requests the second page of one session twice and requires
// the identical node list both times.
func (r searchRun) verifyReplay(ctx context.Context) error {
	first, err := callSearch(ctx, r.driver, r.token, r.entry, accessPhrase, "")
	if err != nil {
		return err
	}
	if first.Cursor == "" {
		return fmt.Errorf("qa datagen: search %q returned no continuation cursor for replay", accessPhrase)
	}
	second, err := callSearch(ctx, r.driver, r.token, r.entry, accessPhrase, first.Cursor)
	if err != nil {
		return err
	}
	replayed, err := callSearch(ctx, r.driver, r.token, r.entry, accessPhrase, first.Cursor)
	if err != nil {
		return err
	}
	if len(second.IDs) == 0 || !slices.Equal(second.IDs, replayed.IDs) {
		return loggedError(ctx, "qa datagen: replay cursor", fmt.Errorf("replayed page returned %v, want %v", replayed.IDs, second.IDs))
	}
	return nil
}

// verifyRevocation opens a session as one member, removes that member from
// the organization, and requires the open cursor and a new search to be
// refused. It then restores the membership with its original role. The
// restore runs after a failed refusal check too.
func (r searchRun) verifyRevocation(ctx context.Context, cfg *config.Config, workspace WorkspaceIdentity) error {
	actor := workspace.Actors[revokedActorIndex]
	first, err := callSearch(ctx, r.driver, actor.Token, r.entry, accessPhrase, "")
	if err != nil {
		return err
	}
	if first.Cursor == "" {
		return fmt.Errorf("qa datagen: member search %q returned no continuation cursor", accessPhrase)
	}
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		return loggedError(ctx, "qa datagen: open postgres for revocation", err)
	}
	defer pool.Close()
	members := postgres.NewOrgMemberRepo(pool)
	member, err := findOrgMember(ctx, members, workspace.OrgID, actor.UserID)
	if err != nil {
		return err
	}
	if err := members.RemoveMember(ctx, workspace.OrgID, actor.UserID); err != nil {
		return loggedError(ctx, "qa datagen: remove member "+actor.UserID.String(), err)
	}
	refusalErr := r.requireRevoked(ctx, actor.Token, first.Cursor)
	restored := &org.Member{ID: uuid.Nil, OrgID: member.OrgID, UserID: member.UserID, Role: member.Role, CreatedAt: time.Time{}}
	if err := members.AddMember(context.WithoutCancel(ctx), restored); err != nil {
		joined := errors.Join(refusalErr, fmt.Errorf("qa datagen: restore member %s: %w", actor.UserID, err))
		slog.ErrorContext(ctx, "qa.datagen.member_restore_failed", slog.String("user_id", actor.UserID.String()),
			slog.String("err", joined.Error()))
		return joined
	}
	return refusalErr
}

// requireRevoked requires the open cursor and a new search to be refused.
// The application caches membership for a bounded lifetime. Each refusal
// check repeats until the convergence deadline.
func (r searchRun) requireRevoked(ctx context.Context, token, cursor string) error {
	if err := r.eventually(ctx, "refuse revoked member cursor", func(ctx context.Context) error {
		return r.refused(ctx, token, r.entry, cursor)
	}); err != nil {
		return err
	}
	return r.eventually(ctx, "refuse revoked member search", func(ctx context.Context) error {
		return r.refused(ctx, token, r.entry, "")
	})
}

// findOrgMember returns the membership row of userID in orgID.
func findOrgMember(ctx context.Context, members *postgres.OrgMemberRepo, orgID, userID uuid.UUID) (*org.Member, error) {
	rows, err := members.ListMembers(ctx, orgID)
	if err != nil {
		return nil, loggedError(ctx, "qa datagen: list members of "+orgID.String(), err)
	}
	for _, member := range rows {
		if member.UserID == userID {
			return member, nil
		}
	}
	return nil, loggedError(ctx, "qa datagen: find member", fmt.Errorf("user %s is not a member of org %s", userID, orgID))
}
