package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const (
	// Each replay or visited record URI uses the session URI followed by
	// sessionReplayURIPart or sessionVisitedURIPart.
	sessionReplayURIPart  = "/replay/"
	sessionVisitedURIPart = "/visited/"
)

// sessionState is one session header and its query tokens. The event of a
// state with nil tokens omits the query_tokens member.
type sessionState struct {
	record searchSessionRecord
	tokens json.RawMessage
}

type sessionReplayValue struct {
	Version   uint64      `json:"version"`
	ResultIDs []uuid.UUID `json:"result_ids"`
	Complete  bool        `json:"complete"`
}

type sessionVisitedValue struct {
	SessionID uuid.UUID `json:"session_id"`
	NodeID    uuid.UUID `json:"node_id"`
}

// sessionAudit records changes for one session writer. The writer reads the
// session header inside its transaction, then calls bind with that header to
// set the event scope.
type sessionAudit struct {
	*mutationAudit
}

// beginSessionAudit reads the audit operation before the first transaction
// attempt of one session writer. It logs and returns an error that wraps
// [audit.ErrMissingMutationContext] when the operation is absent.
func beginSessionAudit(ctx context.Context, operation string, sessionID uuid.UUID) (*sessionAudit, error) {
	changes, err := beginMutationAudit(ctx, uuid.Nil)
	if err != nil {
		return nil, sessionFailure(ctx, operation, sessionID, err)
	}
	return &sessionAudit{mutationAudit: changes}, nil
}

// sessionRecordingFailure handles a search session writer's failure to build
// or save a required change event. If err is not nil, it logs err and the
// session ID at Error and returns err joined with audit.ErrEventNotRecorded.
// [errors.Is] and [errors.As] can find every error in err's chain, including a
// FoundationDB error that transactSession retries. If err is nil, the function
// returns nil without logging.
func sessionRecordingFailure(ctx context.Context, sessionID uuid.UUID, err error) error {
	if err == nil {
		return nil
	}
	slog.ErrorContext(ctx, "search.session.recording_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
	return errors.Join(audit.ErrEventNotRecorded, err)
}

// bind scopes the events of this writer to the organization and workspace of
// record. The scope of the request belongs to the caller. A swept session can
// belong to another workspace.
func (a *sessionAudit) bind(record searchSessionRecord) {
	a.operation.Context.OrgID = record.AuthorityID
	a.operation.Context.WorkspaceID = record.EntryPointID
	a.operation.Context.ScopeID, a.operation.Context.ParentID = uuid.Nil, uuid.Nil
}

// sessionChildTarget returns the resource target of one replay or visited
// record of sessionID. Its ID derives from its URI.
func sessionChildTarget(sessionID uuid.UUID, nodeType, uriSuffix string) audit.Target {
	uri := audit.SearchSessionTarget(sessionID, uuid.Nil).URI + uriSuffix
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: nodeType, Reference: "", Name: "", URI: uri, RelationType: "",
		ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(uri)), SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

func sessionReplayTarget(sessionID uuid.UUID, version uint64) audit.Target {
	return sessionChildTarget(sessionID, audit.TargetSearchSessionReplay, sessionReplayURIPart+strconv.FormatUint(version, 10))
}

func sessionVisitedTarget(sessionID, nodeID uuid.UUID) audit.Target {
	return sessionChildTarget(sessionID, audit.TargetSearchSessionVisited, sessionVisitedURIPart+nodeID.String())
}

func (a *sessionAudit) recordReplayCreate(sessionID uuid.UUID, version uint64, replay searchReplayRecord) error {
	encoded, err := encodeSessionValue(sessionReplayValue{Version: version, ResultIDs: replay.ResultIDs, Complete: replay.Complete})
	if err != nil {
		return err
	}
	return a.record(sessionReplayTarget(sessionID, version), nil, encoded)
}

func (a *sessionAudit) recordReplayDelete(sessionID uuid.UUID, version uint64, replay searchReplayRecord) error {
	encoded, err := encodeSessionValue(sessionReplayValue{Version: version, ResultIDs: replay.ResultIDs, Complete: replay.Complete})
	if err != nil {
		return err
	}
	return a.record(sessionReplayTarget(sessionID, version), encoded, nil)
}

func (a *sessionAudit) recordVisitedCreate(sessionID, nodeID uuid.UUID) error {
	encoded, err := encodeSessionValue(sessionVisitedValue{SessionID: sessionID, NodeID: nodeID})
	if err != nil {
		return err
	}
	return a.record(sessionVisitedTarget(sessionID, nodeID), nil, encoded)
}

func (a *sessionAudit) recordVisitedDelete(sessionID, nodeID uuid.UUID) error {
	encoded, err := encodeSessionValue(sessionVisitedValue{SessionID: sessionID, NodeID: nodeID})
	if err != nil {
		return err
	}
	return a.record(sessionVisitedTarget(sessionID, nodeID), encoded, nil)
}

// encodeSessionValue marshals one audit value to JSON and returns a sessionStepError on failure.
// transactSession logs the error through sessionFailure.
func encodeSessionValue[Value sessionAuditValue](value Value) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, sessionStepError{operation: "encode search session audit value", err: err}
	}
	return encoded, nil
}

type sessionAuditValue interface {
	sessionPlainValue | sessionReplayValue | sessionVisitedValue | audit.ProtectedReference |
		map[string]json.RawMessage | string | []string
}
