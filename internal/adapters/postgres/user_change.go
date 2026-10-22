package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/user"
)

// userValue contains the user record for a change delta, with protected
// references in place of email and display name.
type userValue struct {
	ID          uuid.UUID                `json:"id"`
	Email       audit.ProtectedReference `json:"email"`
	DisplayName audit.ProtectedReference `json:"display_name"`
	IsActive    bool                     `json:"is_active"`
	CreatedAt   time.Time                `json:"created_at"`
	UpdatedAt   time.Time                `json:"updated_at"`
}

// newUserValue builds the created user's delta record and protected email
// and display name values with u.ID as their subject.
func newUserValue(ctx context.Context, u *user.User, isActive bool) (*userValue, []audit.ProtectedValue, error) {
	email, emailValue, err := protectString(ctx, u.ID, u.Email)
	if err != nil {
		return nil, nil, err
	}
	displayName, displayNameValue, err := protectString(ctx, u.ID, u.DisplayName)
	if err != nil {
		return nil, nil, err
	}
	value := &userValue{
		ID: u.ID, Email: email, DisplayName: displayName,
		IsActive: isActive, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
	return value, []audit.ProtectedValue{emailValue, displayNameValue}, nil
}

// protectString JSON-encodes value and returns a new UUIDv7 reference and
// its protected value for subjectID.
func protectString(ctx context.Context, subjectID uuid.UUID, value string) (audit.ProtectedReference, audit.ProtectedValue, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		slog.ErrorContext(ctx, "audit.protected.encode_failed", slog.String("err", err.Error()))
		return audit.ProtectedReference{PIIRef: uuid.Nil}, audit.ProtectedValue{Ref: uuid.Nil, SubjectID: uuid.Nil, Value: nil},
			fmt.Errorf("encode protected value for subject %s: %w", subjectID, err)
	}
	ref := uuid.Must(uuid.NewV7())
	return audit.ProtectedReference{PIIRef: ref}, audit.ProtectedValue{Ref: ref, SubjectID: subjectID, Value: encoded}, nil
}
