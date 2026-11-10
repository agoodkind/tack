package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
)

// insertTokenRow creates a user and token and returns the token ID. Test
// cleanup removes both.
func insertTokenRow(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	hash := make([]byte, 32)
	if _, err := rand.Read(hash); err != nil {
		t.Fatalf("token hash: %v", err)
	}

	userID := uuid.Must(uuid.NewV7())
	tokenID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO users (id, email) VALUES ($1, $2)`,
		userID,
		"token-use-"+userID.String()+"@example.com",
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO api_tokens (id, user_id, token_hash, label) VALUES ($1, $2, $3, 'token-use-test')`,
		tokenID,
		userID,
		hex.EncodeToString(hash),
	); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	return tokenID
}

func makeTokenUsedEvent(orgID, tokenID uuid.UUID, at time.Time) Event {
	return Event{
		EventID: uuid.Must(uuid.NewV7()),
		Verb:    string(VerbAuthTokenUsed),
		Actor:   Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:  Entity{Type: "auth", Name: "token_accepted"},
		Context: EventContext{
			OrgID:      orgID,
			Source:     SourceMCP,
			APITokenID: tokenID,
		},
		Outcome:    OutcomeOK,
		OccurredAt: at,
	}
}

func lastUsedOf(t *testing.T, pool *pgxpool.Pool, tokenID uuid.UUID) *time.Time {
	t.Helper()
	var lastUsed *time.Time
	if err := pool.QueryRow(
		context.Background(),
		`SELECT last_used FROM api_tokens WHERE id = $1`,
		tokenID,
	).Scan(
		&lastUsed,
	); err != nil {
		t.Fatalf("read last_used: %v", err)
	}

	return lastUsed
}

// TestConsumerProjectsTokenLastUseFromTheAuthEvent processes authentication
// events for one token with the ledger writer login. It checks that last-use
// matches the newest event when a later record contains an older event.
func TestConsumerProjectsTokenLastUseFromTheAuthEvent(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })
	tokenID := insertTokenRow(t, pool)
	if lastUsedOf(t, pool, tokenID) != nil {
		t.Fatal("a fresh token has no last use")
	}

	// A fresh ledger does not have a partition before its migration week's
	// Monday. Both events use times at or after the current time.
	older := clock.Now().UTC().Truncate(time.Microsecond)
	newer := older.Add(time.Hour)
	produceEvents(
		t,
		brokers,
		topic,
		[]Event{
			makeTokenUsedEvent(orgID, tokenID, newer),
			makeTokenUsedEvent(orgID, tokenID, older),
		},
	)
	runConsumerOnce(
		t,
		ConsumerConfig{
			Brokers:      brokers,
			Topic:        topic,
			GroupID:      "tack-audit-projector-test-" + uuid.NewString()[:8],
			BatchSize:    32,
			PollInterval: 100 * time.Millisecond,
			YugabyteDSN:  writerLoginDSN(t, pool, integrationDSN(t)),
		},
		orgID,
		2,
	)

	got := lastUsedOf(t, pool, tokenID)
	if got == nil || !got.Equal(newer) {
		t.Fatalf("last_used = %v, want %s from the newest auth event", got, newer)
	}
}

// TestAppRoleCannotWriteTheTokenTable checks that the app_auth role cannot
// update last_used under migration 015. It also checks that the role can
// still read the token row.
func TestAppRoleCannotWriteTheTokenTable(t *testing.T) {
	pool, _, _ := newConsumerEnv(t)
	ctx := context.Background()
	tokenID := insertTokenRow(t, pool)

	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SET ROLE app_auth`); err != nil {
		t.Fatalf("set role app_auth: %v", err)
	}

	defer func() { _, _ = connection.Exec(ctx, `RESET ROLE`) }()

	_, err = connection.Exec(ctx, `UPDATE api_tokens SET last_used = now() WHERE id = $1`, tokenID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("app_auth updated api_tokens (err = %v), want permission denied", err)
	}

	var readable int
	if err := connection.QueryRow(
		ctx,
		`SELECT count(*) FROM api_tokens WHERE id = $1`,
		tokenID,
	).Scan(
		&readable,
	); err != nil ||
		readable != 1 {
		t.Fatalf("app_auth must still read its token row: count %d, err %v", readable, err)
	}
}

// TestNewestTokenUsesFoldsABatchPerToken checks that the projection selects
// one latest time per token and omits events that did not accept a token.
func TestNewestTokenUsesFoldsABatchPerToken(t *testing.T) {
	tokenA := uuid.Must(uuid.NewV7())
	tokenB := uuid.Must(uuid.NewV7())
	base := time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC)
	projected := []committedAuthEvent{
		{Action: string(VerbAuthTokenUsed), APITokenID: tokenA, EventTime: base},
		{
			Action:     string(VerbAuthTokenUsed),
			APITokenID: tokenA,
			EventTime:  base.Add(2 * time.Second),
		},
		{Action: string(VerbAuthTokenUsed), APITokenID: tokenA, EventTime: base.Add(time.Second)},
		{Action: string(VerbAuthTokenUsed), APITokenID: tokenB, EventTime: base},
		{Action: string(VerbNodeRead), APITokenID: tokenB, EventTime: base.Add(time.Hour)},
		{Action: string(VerbAuthTokenUsed), APITokenID: uuid.Nil, EventTime: base.Add(time.Hour)},
	}
	newest := newestTokenUses(projected)
	if len(newest) != 2 {
		t.Fatalf("folded %d tokens, want 2", len(newest))
	}

	if !newest[tokenA].Equal(base.Add(2 * time.Second)) {
		t.Fatalf("token A newest = %s, want +2s", newest[tokenA])
	}

	if !newest[tokenB].Equal(base) {
		t.Fatalf("token B newest = %s, want base", newest[tokenB])
	}

	ids := sortedTokenIDs(newest)
	if len(ids) != 2 || ids[0].String() > ids[1].String() {
		t.Fatalf("token ids are not in a fixed order: %v", ids)
	}
}
