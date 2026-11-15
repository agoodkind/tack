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

// Create stores a session if its authority still writes the access version.
func (s *SearchSessionStore) Create(
	ctx context.Context,
	session searchdomain.Session,
) (created searchdomain.Session, err error) {
	defer telemetry.FDBOp(ctx, "store.search_session.create")(&err)
	if session.ID == uuid.Nil || session.Version != 0 || len(session.Snapshot.QueryTokens) == 0 ||
		session.Snapshot.PITID == "" {
		return searchdomain.Session{}, sessionFailure(
			ctx,
			"create search session",
			session.ID,
			errors.New("session identity, snapshot, and tokens are required"),
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
	err = transactSession(
		ctx,
		s.db,
		s.clock,
		"create search session",
		session.ID,
		func(tr fdb.Transaction) error {
			return createSession(ctx, tr, session, record)
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
	tr fdb.Transaction,
	session searchdomain.Session,
	record searchSessionRecord,
) error {
	var existing searchSessionRecord
	found, readErr := readSessionValue(tr, searchSessionKey(session.ID), &existing)
	if readErr != nil {
		return readErr
	}

	if found {
		return fmt.Errorf("session %s already exists", session.ID)
	}

	if versionErr := requireWrittenVersion(
		ctx,
		tr,
		session.AuthorityID,
		session.Query.Access.Version,
	); versionErr != nil {
		return versionErr
	}

	if writeErr := writeSessionValue(
		tr,
		searchSessionKey(session.ID),
		record,
		maxSessionHeaderBytes,
	); writeErr != nil {
		return writeErr
	}

	tokens := []byte(session.Snapshot.QueryTokens)

	for chunk := range record.TokenChunks {
		end := min(len(tokens), (chunk+1)*sessionTokenChunkBytes)
		tr.Set(
			fdb.Key(searchSessionTokenKey(session.ID, chunk)),
			tokens[chunk*sessionTokenChunkBytes:end],
		)
	}

	tr.Set(fdb.Key(searchSessionExpiryKey(session.ID, session.IdleDeadline)), nil)
	tr.Set(fdb.Key(searchSessionPresenceKey(session.Query.Index, session.ID)), nil)
	tr.Set(
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
