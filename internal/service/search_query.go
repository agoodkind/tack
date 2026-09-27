package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// QueryAccessPolicy compiles caller access for one entry point as opaque values.
type QueryAccessPolicy interface {
	EntryAuthority(context.Context, uuid.UUID) (uuid.UUID, error)
	Query(context.Context, searchaccess.AccessRequest) (searchdomain.AccessFilter, error)
}

// ExpiredSessionLister lists sessions past their idle deadline for cleanup.
type ExpiredSessionLister interface {
	ExpiredSessions(context.Context, int) ([]uuid.UUID, error)
}

// SearchQueryPorts are the production boundaries of public ranked search.
type SearchQueryPorts struct {
	Ranker    searchdomain.Ranker
	Sessions  searchdomain.SessionStore
	Expired   ExpiredSessionLister
	Summaries searchdomain.SummaryReader
	Access    QueryAccessPolicy
	Index     searchdomain.ServingIndexReader
}

// SearchRequest is one authenticated search call. A zero SessionID opens a
// new session. Otherwise Version is the cursor's session version.
type SearchRequest struct {
	PrincipalID, EntryPointID uuid.UUID
	MemberOrganizations       []uuid.UUID
	Text, NodeType            string
	SessionID                 uuid.UUID
	Version                   uint64
}

// SearchPage is one public response. NextVersion is the cursor version of
// the following page when Complete is false.
type SearchPage struct {
	SessionID   uuid.UUID
	NextVersion uint64
	Results     []node.Summary
	Complete    bool
}

// SearchQueryService runs ranked search over durable sessions. It keeps no
// process-local session state.
type SearchQueryService struct {
	ports    SearchQueryPorts
	clock    clock.Clock
	settings config.SearchQuerySettings
}

// NewSearchQueryService constructs the public search service.
func NewSearchQueryService(ports SearchQueryPorts, source clock.Clock, settings config.SearchQuerySettings) *SearchQueryService {
	return &SearchQueryService{ports: ports, clock: source, settings: settings}
}

// Search opens a session or continues one, then returns one bounded page of
// currently authorized nodes.
func (s *SearchQueryService) Search(ctx context.Context, request SearchRequest) (SearchPage, error) {
	ctx, cancel := context.WithTimeout(ctx, s.settings.RequestDeadline)
	defer cancel()
	text := strings.TrimSpace(request.Text)
	if text == "" || len(text) > s.settings.MaxQueryBytes || !utf8.ValidString(text) {
		return SearchPage{}, queryFailure(ctx, "validate query", request.SessionID,
			fmt.Errorf("query must contain 1 to %d bytes of UTF-8 text: %w", s.settings.MaxQueryBytes, searchdomain.ErrInvalidQuery))
	}
	authority, err := s.ports.Access.EntryAuthority(ctx, request.EntryPointID)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "resolve entry point authority", request.SessionID, err)
	}
	filter, err := s.ports.Access.Query(ctx, searchaccess.AccessRequest{
		Version: "", PrincipalID: request.PrincipalID, AuthorityID: authority,
		EntryPointID: request.EntryPointID, MemberOrganizations: request.MemberOrganizations,
	})
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "compile caller access", request.SessionID, err)
	}
	if request.SessionID == uuid.Nil {
		page, openErr := s.open(ctx, request, authority, text, filter)
		s.sweepExpired(ctx)
		return page, openErr
	}
	return s.continueSession(ctx, request, authority, text, filter)
}

func (s *SearchQueryService) open(ctx context.Context, request SearchRequest, authority uuid.UUID, text string, filter searchdomain.AccessFilter) (SearchPage, error) {
	index, err := s.ports.Index.ServingSearchIndex(ctx)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "read serving search index", uuid.Nil, err)
	}
	query := searchdomain.Query{Text: text, Index: index, NodeType: request.NodeType, Access: filter}
	snapshot, err := s.ports.Ranker.Open(ctx, query)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "open search snapshot", uuid.Nil, err)
	}
	now := s.clock.Now().UTC()
	session := searchdomain.Session{
		ID: uuid.Must(uuid.NewV7()), PrincipalID: request.PrincipalID, AuthorityID: authority,
		EntryPointID: request.EntryPointID,
		Binding:      searchdomain.SessionBinding(request.PrincipalID, authority, request.EntryPointID, query),
		Query:        query, Snapshot: snapshot, Sort: nil, Version: 0, CreatedAt: now,
		IdleDeadline: now.Add(s.settings.IdleTimeout), AbsoluteDeadline: now.Add(s.settings.AbsoluteTimeout),
		Complete: false, Closing: false,
	}
	created, err := s.ports.Sessions.Create(ctx, session)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "create search session", session.ID, errors.Join(err, s.ports.Ranker.Close(ctx, snapshot)))
	}
	return s.advance(ctx, created, filter)
}

func (s *SearchQueryService) continueSession(ctx context.Context, request SearchRequest, authority uuid.UUID, text string, filter searchdomain.AccessFilter) (SearchPage, error) {
	session, err := s.ports.Sessions.Load(ctx, request.SessionID)
	if err != nil {
		return SearchPage{}, queryFailure(ctx, "load search session", request.SessionID, err)
	}
	current := searchdomain.Query{Text: text, Index: session.Query.Index, NodeType: request.NodeType, Access: filter}
	binding := searchdomain.SessionBinding(request.PrincipalID, authority, request.EntryPointID, current)
	if binding != session.Binding || request.Version > session.Version {
		return SearchPage{}, queryFailure(ctx, "verify search session", session.ID, searchdomain.ErrSessionMismatch)
	}
	if session.Closing || session.Expired(s.clock.Now()) {
		return SearchPage{}, queryFailure(ctx, "verify search session", session.ID, searchdomain.ErrSessionExpired)
	}
	if request.Version < session.Version {
		return s.replay(ctx, session, request.Version, filter)
	}
	if session.Complete {
		return SearchPage{}, queryFailure(ctx, "verify search session", session.ID, searchdomain.ErrSessionExpired)
	}
	return s.advance(ctx, session, filter)
}

// queryFailure wraps err. Expected caller and session outcomes log at Info.
// Storage, engine, and policy failures log at Error.
func queryFailure(ctx context.Context, operation string, sessionID uuid.UUID, err error) error {
	wrapped := fmt.Errorf("search %s: %w", operation, err)
	expected := []error{
		searchdomain.ErrInvalidQuery, searchdomain.ErrSessionChanged, searchdomain.ErrSessionExpired,
		searchdomain.ErrSessionNotFound, searchdomain.ErrSessionMismatch, searchdomain.ErrSnapshotLost,
		searchdomain.ErrNoServingIndex, searchaccess.ErrAccessDenied,
	}
	for _, outcome := range expected {
		if errors.Is(err, outcome) {
			telemetry.L(ctx).InfoContext(ctx, "search.query.refused", slog.String("reason", wrapped.Error()), slog.String("session_id", sessionID.String()))
			return wrapped
		}
	}
	telemetry.L(ctx).ErrorContext(ctx, "search.query.failed", slog.String("err", wrapped.Error()), slog.String("session_id", sessionID.String()))
	return wrapped
}
