package auth

import (
	"context"
	"sync"
)

const (
	// RejectionMissingAuthorization is the rejection reason when a request does not include a bearer token.
	RejectionMissingAuthorization = "missing_authorization"
	// RejectionBearerRefused is the rejection reason when the token store rejects a bearer value.
	RejectionBearerRefused = "bearer_refused"
	// RejectionBearerCheckFailed is the rejection reason when a bearer check fails before returning a result.
	RejectionBearerCheckFailed = "bearer_check_failed"
	// RejectionDevIdentityInvalid is the rejection reason when a development Authorization value is not a UUID.
	RejectionDevIdentityInvalid = "dev_identity_not_uuid"
)

type rejectionKey struct{}

type rejectionNote struct {
	mu     sync.Mutex
	reason string
}

// WithRejectionNote returns a context with an empty rejection note.
// The Bearer and DevBearer middleware store the rejection reason in that note when they reject a request.
func WithRejectionNote(ctx context.Context) context.Context {
	return context.WithValue(ctx, rejectionKey{}, &rejectionNote{mu: sync.Mutex{}, reason: ""})
}

// RejectionReason returns the authentication rejection reason from ctx.
// It returns false when ctx does not contain a rejection reason.
func RejectionReason(ctx context.Context) (string, bool) {
	note, _ := ctx.Value(rejectionKey{}).(*rejectionNote)
	if note == nil {
		return "", false
	}
	note.mu.Lock()
	defer note.mu.Unlock()
	return note.reason, note.reason != ""
}

func noteRejection(ctx context.Context, reason string) {
	note, _ := ctx.Value(rejectionKey{}).(*rejectionNote)
	if note == nil {
		return
	}
	note.mu.Lock()
	defer note.mu.Unlock()
	note.reason = reason
}
