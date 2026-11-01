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

// verifySessionLifecycle runs every search session writer on the target. It
// traverses one session to completion and opens a second session that it
// abandons. It waits past the end of the expiry minute of the abandoned
// session and runs a new search that sweeps it. The cursor of the swept
// session must fail.
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
	if _, err := callSearch(ctx, r.driver, r.identities(), r.token, r.entry, query, abandoned.Cursor); err == nil {
		return loggedError(ctx, "qa datagen: sweep the abandoned session", errors.New("the cursor of the abandoned session returned a page"))
	}
	return nil
}

// waitForSessionExpiry waits until instant or the end of ctx.
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
