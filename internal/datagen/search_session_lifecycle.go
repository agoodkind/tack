package datagen

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goodkind.io/tack/internal/clock"
)

// sessionSweepMargin follows the end of the expiry minute of the abandoned
// session before the search that sweeps it.
const sessionSweepMargin = time.Second

const searchSessionExpiredResponse = "#### Request could not be completed\n\n" +
	"- Problem: the search cursor expired; start a new search without cursor\n\n" +
	"Next step: Fix the request and retry once."

// verifySessionLifecycle runs every search session writer on the target. It
// traverses one session to completion and opens a second session that it
// abandons. It waits past the end of the expiry minute of the abandoned
// session and runs a new search that sweeps it. The cursor of the swept
// session must return the expired-session response.
func (r searchRun) verifySessionLifecycle(ctx context.Context) error {
	query := r.phrases.Continuation
	if _, err := traverseSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, r.limits); err != nil {
		return err
	}
	abandoned, err := callSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, "")
	if err != nil {
		return err
	}
	if abandoned.Cursor == "" {
		return fmt.Errorf("qa datagen: search %q returned no continuation cursor to abandon", query)
	}
	expiry := clock.Now().Add(r.limits.IdleTimeout).Truncate(time.Minute).Add(time.Minute + sessionSweepMargin)
	if err := waitForSessionExpiry(ctx, expiry); err != nil {
		return err
	}
	if _, err := traverseSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, r.limits); err != nil {
		return err
	}
	return r.requireExpiredSession(ctx, query, abandoned.Cursor)
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
