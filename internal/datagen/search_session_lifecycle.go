package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// sessionSweepMargin follows the end of the expiry minute of the abandoned
// session before the search that sweeps it.
const sessionSweepMargin = time.Second

const searchSessionExpiredResponse = "#### Request could not be completed\n\n" +
	"- Problem: the search cursor expired\n\n" +
	"Next step: Start a new search without the `cursor` parameter."

// verifySessionLifecycle runs every search session writer on the target.
// It traverses one session to completion and abandons a second session.
// After the abandoned session's expiry minute, it runs new searches until
// a sweep removes the header. It requires the cursor to return the same
// expired-session response for expired and absent sessions. It returns
// the abandoned session ID for the caller to check the ledger.
func (r searchRun) verifySessionLifecycle(ctx context.Context, cursors *tools.SearchCursorCodec) (uuid.UUID, error) {
	query := r.phrases.Continuation
	if _, err := traverseSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, r.limits); err != nil {
		return uuid.Nil, err
	}
	abandoned, err := callSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, "")
	if err != nil {
		return uuid.Nil, err
	}
	if abandoned.Cursor == "" {
		return uuid.Nil, fmt.Errorf("qa datagen: search %q returned no continuation cursor to abandon", query)
	}
	sessionID, _, err := cursors.Decode(abandoned.Cursor)
	if err != nil {
		return uuid.Nil, loggedError(ctx, "qa datagen: decode the cursor of the abandoned session", err)
	}
	slog.InfoContext(ctx, "qa.datagen.session_abandoned", slog.String("session_id", sessionID.String()))
	expiry := clock.Now().Add(r.limits.IdleTimeout).Truncate(time.Minute).Add(time.Minute + sessionSweepMargin)
	if err := waitForSessionExpiry(ctx, expiry); err != nil {
		return uuid.Nil, err
	}
	if err := r.eventually(ctx, "sweep the abandoned session", func(ctx context.Context) error {
		return r.sweepAbandonedSession(ctx, query, sessionID)
	}); err != nil {
		return uuid.Nil, err
	}
	return sessionID, r.requireExpiredSession(ctx, query, abandoned.Cursor)
}

// sweepAbandonedSession runs one new search and requires the session
// header to be absent afterward. Each new search sweeps at most four
// expired sessions. Sessions from earlier searches in the run can use
// that limit first.
func (r searchRun) sweepAbandonedSession(ctx context.Context, query string, sessionID uuid.UUID) error {
	if _, err := traverseSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, r.limits); err != nil {
		return err
	}
	_, err := r.fixture.stores.SearchSessions(clock.Wall{}, r.limits.IdleTimeout).Load(ctx, sessionID)
	if errors.Is(err, searchdomain.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return loggedError(ctx, "qa datagen: load the abandoned session "+sessionID.String(), err)
	}
	return fmt.Errorf("the header of the abandoned session %s remains after the search", sessionID)
}

func (r searchRun) requireExpiredSession(ctx context.Context, query, cursor string) error {
	_, err := callSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, cursor)
	if err == nil {
		return loggedError(ctx, "qa datagen: sweep the abandoned session", errors.New("the cursor of the abandoned session returned a page"))
	}
	var toolError *toolCallError
	if errors.As(err, &toolError) && toolError.toolName == "tack_search" && toolError.message == searchSessionExpiredResponse {
		return nil
	}
	return loggedError(ctx, "qa datagen: verify the expired-session response", err)
}

func waitForSessionExpiry(ctx context.Context, instant time.Time) error {
	timer := time.NewTimer(instant.Sub(clock.Now()))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return loggedError(ctx, "qa datagen: wait for the session expiry", ctx.Err())
	case <-timer.C:
		return nil
	}
}
