package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// errAdmissionWaitEnded means every search slot of the process stayed busy
// for the whole admission wait.
var errAdmissionWaitEnded = errors.New("no search slot became free within the admission wait")

// acquireSearchSlot waits for one of the Concurrency search slots of this
// process. It waits at most AdmissionWait and stops early when ctx ends. On
// success it returns the function that frees the slot. Otherwise it returns
// an error that wraps [searchdomain.ErrEngineUnavailable], and the search
// does not send any request to OpenSearch.
func (s *SearchQueryService) acquireSearchSlot(ctx context.Context, sessionID uuid.UUID) (func(), error) {
	timer := time.NewTimer(s.settings.AdmissionWait)
	defer timer.Stop()
	var cause error
	select {
	case s.slots <- struct{}{}:
		return s.releaseSearchSlot, nil
	case <-timer.C:
		cause = errAdmissionWaitEnded
	case <-ctx.Done():
		cause = ctx.Err()
	}
	refused := fmt.Errorf("search admission with %d slots: %w: %w", s.settings.Concurrency, cause, searchdomain.ErrEngineUnavailable)
	slog.ErrorContext(ctx, "search.admission.refused", slog.String("err", refused.Error()),
		slog.String("session_id", sessionID.String()), slog.Int("concurrency", s.settings.Concurrency),
		slog.Duration("admission_wait", s.settings.AdmissionWait))
	return nil, refused
}

// releaseSearchSlot frees one search slot that acquireSearchSlot took.
func (s *SearchQueryService) releaseSearchSlot() {
	<-s.slots
}
