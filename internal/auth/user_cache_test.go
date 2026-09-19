package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/user"
)

// countingUsers is a users table that counts its by-id reads, the number the
// user cache exists to reduce.
type countingUsers struct {
	records map[uuid.UUID]*user.User
	reads   atomic.Int64
}

func (c *countingUsers) GetByID(_ context.Context, id uuid.UUID) (*user.User, error) {
	c.reads.Add(1)
	record, ok := c.records[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	copied := *record
	return &copied, nil
}

func (c *countingUsers) GetByEmail(context.Context, string) (*user.User, error) {
	return nil, domain.ErrNotFound
}

func (c *countingUsers) Create(_ context.Context, u *user.User) (*user.User, error) {
	c.records[u.ID] = u
	return u, nil
}

func namedUser(name string) *user.User {
	return &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: name + "@example.test", DisplayName: name, AvatarURL: nil,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{},
	}
}

// TestCachedUsersReadsOncePerLifetime pins the production reading of
// 2026-09-19: a tool call that renders "Created by" read the users table on
// every call. Within the lifetime the second render costs no ledger read.
func TestCachedUsersReadsOncePerLifetime(t *testing.T) {
	creator := namedUser("creator")
	store := &countingUsers{records: map[uuid.UUID]*user.User{creator.ID: creator}}
	cached := NewCachedUsers(store, time.Minute, 16)
	ctx := context.Background()

	for range 3 {
		got, err := cached.GetByID(ctx, creator.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.DisplayName != "creator" {
			t.Fatalf("display name = %q, want creator", got.DisplayName)
		}
	}
	if reads := store.reads.Load(); reads != 1 {
		t.Fatalf("users table read %d times, want 1", reads)
	}
}

// TestCachedUsersSeesAChangeAfterTheLifetime pins the staleness bound: a
// display-name change made elsewhere is seen once the lifetime passes.
func TestCachedUsersSeesAChangeAfterTheLifetime(t *testing.T) {
	creator := namedUser("before")
	store := &countingUsers{records: map[uuid.UUID]*user.User{creator.ID: creator}}
	cached := NewCachedUsers(store, 40*time.Millisecond, 16)
	ctx := context.Background()

	if _, err := cached.GetByID(ctx, creator.ID); err != nil {
		t.Fatalf("first get: %v", err)
	}
	store.records[creator.ID] = &user.User{
		ID: creator.ID, Email: creator.Email, DisplayName: "after", AvatarURL: nil,
		CreatedAt: time.Time{}, UpdatedAt: time.Time{},
	}
	time.Sleep(60 * time.Millisecond)
	got, err := cached.GetByID(ctx, creator.ID)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if got.DisplayName != "after" {
		t.Fatalf("display name = %q after the lifetime, want after", got.DisplayName)
	}
}

// TestCachedUsersNeverCachesAMiss pins that an unknown id is asked again,
// so a user created after the miss renders on the next call.
func TestCachedUsersNeverCachesAMiss(t *testing.T) {
	store := &countingUsers{records: map[uuid.UUID]*user.User{}}
	cached := NewCachedUsers(store, time.Minute, 16)
	ctx := context.Background()
	later := namedUser("later")

	if _, err := cached.GetByID(ctx, later.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown user: err = %v, want not found", err)
	}
	store.records[later.ID] = later
	if _, err := cached.GetByID(ctx, later.ID); err != nil {
		t.Fatalf("a user created after a miss was not found: %v", err)
	}
}
