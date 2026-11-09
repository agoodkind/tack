package auth

import (
	"context"
	"sync"
)

const (
	// RejectionMissingAuthorization is the reason for a missing bearer value,
	// including an empty value or a header without the Bearer prefix.
	RejectionMissingAuthorization = "missing_authorization"
	// RejectionBearerRefused is the reason for a token validator refusal.
	RejectionBearerRefused = "bearer_refused"
	// RejectionBearerCheckFailed is the reason when a token check cannot
	// determine validity.
	RejectionBearerCheckFailed = "bearer_check_failed"
	// RejectionDevIdentityInvalid is the reason when a development bearer
	// value does not parse as a UUID.
	RejectionDevIdentityInvalid = "dev_identity_not_uuid"
)

type rejectionKey struct{}

type rejectionNote struct {
	mu     sync.Mutex
	reason string
}

// WithRejectionNote returns a context with an empty rejection note.
//
// Bearer and DevBearer store token-check rejection reasons in that note. The
// note permits later inspection through RejectionReason.
//
// Reading a new note returns an empty reason and false:
//
//	ctx = WithRejectionNote(ctx)
//	reason, rejected := RejectionReason(ctx)
func WithRejectionNote(ctx context.Context) context.Context {
	return context.WithValue(ctx, rejectionKey{}, &rejectionNote{mu: sync.Mutex{}, reason: ""})
}

// RejectionReason returns the authentication rejection reason from ctx.
//
// It locks an existing note's mutex while reading that note.
//
// The return values distinguish these states:
//   - An absent or empty note returns an empty reason and false.
//   - A note with a token-check rejection returns that reason and true.
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
