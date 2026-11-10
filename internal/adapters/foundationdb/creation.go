package foundationdb

import (
	"context"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func writeSessionParts(
	transaction fdb.Transaction,
	session searchdomain.Session,
	record searchSessionRecord,
) {
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
}

func appendSessionCreation(
	ctx context.Context,
	transaction fdb.Transaction,
	session searchdomain.Session,
	record searchSessionRecord,
	changes *sessionAudit,
) error {
	if recordErr := changes.recordSessionCreate(sessionState{
		record: record,
		tokens: session.Snapshot.QueryTokens,
	}); recordErr != nil {
		return sessionRecordingFailure(ctx, session.ID, recordErr)
	}

	if appendErr := changes.append(ctx, transaction); appendErr != nil {
		return sessionRecordingFailure(ctx, session.ID, appendErr)
	}

	slog.DebugContext(
		ctx,
		"search.session.create_commit_ready",
		slog.String("session_id", session.ID.String()),
	)

	return nil
}
