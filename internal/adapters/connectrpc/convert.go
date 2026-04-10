// Package connectrpc provides Connect-RPC handlers for all Tack services.
// It translates between domain types and protobuf messages and maps domain
// errors to Connect status codes.
package connectrpc

import (
	"time"

	v1 "goodkind.io/tack/gen/tack/v1"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/state"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ── shared helpers ────────────────────────────────────────────────────────────

func baseFromDomain(b domain.Base) *v1.Base {
	pb := &v1.Base{
		Id:        b.ID.String(),
		CreatedAt: timestamppb.New(b.CreatedAt),
		UpdatedAt: timestamppb.New(b.UpdatedAt),
		CreatedBy: b.CreatedBy.String(),
	}
	if b.UpdatedBy != nil {
		s := b.UpdatedBy.String()
		pb.UpdatedBy = &s
	}
	return pb
}

// baseFromFields constructs a Base for domain types that don't embed domain.Base.
func baseFromFields(id uuid.UUID, createdAt, updatedAt time.Time, createdBy *uuid.UUID, updatedBy *uuid.UUID) *v1.Base {
	pb := &v1.Base{
		Id:        id.String(),
		CreatedAt: timestamppb.New(createdAt),
		UpdatedAt: timestamppb.New(updatedAt),
	}
	if createdBy != nil {
		pb.CreatedBy = createdBy.String()
	}
	if updatedBy != nil {
		s := updatedBy.String()
		pb.UpdatedBy = &s
	}
	return pb
}

func mustUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}

func optUUID(s *string) *uuid.UUID {
	if s == nil {
		return nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil
	}
	return &id
}

func optTS(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

func toTS(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func uuidSlice(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func parseUUIDs(ss []string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		if id, err := uuid.Parse(s); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// ── Priority ──────────────────────────────────────────────────────────────────

func protoPriority(p issue.Priority) v1.Priority {
	switch p {
	case issue.PriorityUrgent:
		return v1.Priority_PRIORITY_URGENT
	case issue.PriorityHigh:
		return v1.Priority_PRIORITY_HIGH
	case issue.PriorityMedium:
		return v1.Priority_PRIORITY_MEDIUM
	case issue.PriorityLow:
		return v1.Priority_PRIORITY_LOW
	default:
		return v1.Priority_PRIORITY_NONE
	}
}

func domainPriority(p v1.Priority) issue.Priority {
	switch p {
	case v1.Priority_PRIORITY_URGENT:
		return issue.PriorityUrgent
	case v1.Priority_PRIORITY_HIGH:
		return issue.PriorityHigh
	case v1.Priority_PRIORITY_MEDIUM:
		return issue.PriorityMedium
	case v1.Priority_PRIORITY_LOW:
		return issue.PriorityLow
	default:
		return issue.PriorityNone
	}
}

// ── StateGroup ────────────────────────────────────────────────────────────────

func protoStateGroup(g state.GroupName) v1.StateGroup {
	switch g {
	case state.GroupBacklog:
		return v1.StateGroup_STATE_GROUP_BACKLOG
	case state.GroupTodo:
		return v1.StateGroup_STATE_GROUP_TODO
	case state.GroupStarted:
		return v1.StateGroup_STATE_GROUP_STARTED
	case state.GroupCompleted:
		return v1.StateGroup_STATE_GROUP_COMPLETED
	case state.GroupCancelled:
		return v1.StateGroup_STATE_GROUP_CANCELLED
	default:
		return v1.StateGroup_STATE_GROUP_UNSPECIFIED
	}
}

func domainStateGroup(g v1.StateGroup) state.GroupName {
	switch g {
	case v1.StateGroup_STATE_GROUP_BACKLOG:
		return state.GroupBacklog
	case v1.StateGroup_STATE_GROUP_TODO:
		return state.GroupTodo
	case v1.StateGroup_STATE_GROUP_STARTED:
		return state.GroupStarted
	case v1.StateGroup_STATE_GROUP_COMPLETED:
		return state.GroupCompleted
	case v1.StateGroup_STATE_GROUP_CANCELLED:
		return state.GroupCancelled
	default:
		return state.GroupBacklog
	}
}
