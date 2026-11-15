package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

var (
	errSearchSessionInput  = errors.New("session identity, snapshot, and tokens are required")
	errSearchSessionExists = errors.New("already exists")
)

// Create stores a session if its authority still writes the access version.
//
// The header, query tokens, and deadline indexes commit together.
// A duplicate session or outdated access version prevents the write.
//
// For example:
//
//	created, err := store.Create(ctx, session)
func (s *SearchSessionStore) Create(
	ctx context.Context,
	session searchdomain.Session,
) (searchdomain.Session, error) {
	var createErr error
	defer telemetry.FDBOp(ctx, "store.search_session.create")(&createErr)
	created, createErr := s.create(ctx, session)

	return created, createErr
}

func (s *SearchSessionStore) create(
	ctx context.Context,
	session searchdomain.Session,
) (searchdomain.Session, error) {
	if session.ID == uuid.Nil || session.Version != 0 || len(session.Snapshot.QueryTokens) == 0 ||
		session.Snapshot.PITID == "" {
		return searchdomain.Session{}, sessionFailure(
			ctx,
			"create search session",
			session.ID,
			errSearchSessionInput,
		)
	}

	if len(session.Snapshot.QueryTokens) > maxSessionTokenBytes {
		return searchdomain.Session{}, sessionFailure(
			ctx,
			"create search session",
			session.ID,
			fmt.Errorf(
				"query tokens contain %d bytes, above the %d-byte bound: %w",
				len(session.Snapshot.QueryTokens),
				maxSessionTokenBytes,
				searchdomain.ErrInvalidQuery,
			),
		)
	}

	record := sessionRecordFor(session)
	err := transactSession(
		ctx,
		s.db,
		s.clock,
		"create search session",
		session.ID,
		func(transaction fdb.Transaction) error {
			return createSession(ctx, transaction, session, record)
		},
	)
	if err != nil {
		return searchdomain.Session{}, err
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"search.session.created",
		slog.String("session_id", session.ID.String()),
		slog.String("index", session.Query.Index),
		slog.Int("token_bytes", record.TokenBytes),
	)

	return session, nil
}

func createSession(
	ctx context.Context,
	transaction fdb.Transaction,
	session searchdomain.Session,
	record searchSessionRecord,
) error {
	var existing searchSessionRecord
	found, readErr := readSessionValue(transaction, searchSessionKey(session.ID), &existing)
	if readErr != nil {
		return readErr
	}

	if found {
		duplicateErr := fmt.Errorf("session %s %w", session.ID, errSearchSessionExists)
		slog.ErrorContext(
			ctx,
			"search.session.storage_failed",
			slog.String("err", duplicateErr.Error()),
			slog.String("operation", "create search session"),
			slog.String("session_id", session.ID.String()),
		)

		return duplicateErr
	}

	if versionErr := requireWrittenVersion(
		ctx,
		transaction,
		session.AuthorityID,
		session.Query.Access.Version,
	); versionErr != nil {
		return versionErr
	}

	if writeErr := writeSessionValue(
		transaction,
		searchSessionKey(session.ID),
		record,
		maxSessionHeaderBytes,
	); writeErr != nil {
		return writeErr
	}

	tokens := []byte(session.Snapshot.QueryTokens)

	for chunk := range record.TokenChunks {
		end := min(len(tokens), (chunk+1)*sessionTokenChunkBytes)
		transaction.Set(
			fdb.Key(searchSessionTokenKey(session.ID, chunk)),
			tokens[chunk*sessionTokenChunkBytes:end],
		)
	}

	transaction.Set(fdb.Key(searchSessionExpiryKey(session.ID, session.IdleDeadline)), nil)
	transaction.Set(fdb.Key(searchSessionPresenceKey(session.Query.Index, session.ID)), nil)
	transaction.Set(
		fdb.Key(
			searchSessionVersionKey(
				session.AuthorityID,
				session.Query.Access.Version,
				session.AbsoluteDeadline,
				session.ID,
			),
		),
		nil,
	)
	slog.DebugContext(
		ctx,
		"search.session.create_commit_ready",
		slog.String("session_id", session.ID.String()),
	)

	return nil
}
