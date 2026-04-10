package connectrpc

import (
	"time"

	v1 "github.com/agoodkind/tack/gen/tack/v1"
	"github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/cycle"
	"github.com/agoodkind/tack/internal/domain/epic"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/agoodkind/tack/internal/domain/label"
	"github.com/agoodkind/tack/internal/domain/module"
	"github.com/agoodkind/tack/internal/domain/project"
	"github.com/agoodkind/tack/internal/domain/state"
	"github.com/agoodkind/tack/internal/domain/workspace"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ── helpers ──────────────────────────────────────────────────────────────────

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

// ── Workspace ─────────────────────────────────────────────────────────────────

func protoWorkspace(w *workspace.Workspace) *v1.Workspace {
	return &v1.Workspace{
		Base:   baseFromFields(w.ID, w.CreatedAt, w.UpdatedAt, nil, nil),
		OrgId:  w.OrgID.String(),
		Name:   w.Name,
		Slug:   w.Slug,
	}
}

// ── Project ───────────────────────────────────────────────────────────────────

func protoProject(p *project.Project) *v1.Project {
	pb := &v1.Project{
		Base:        baseFromFields(p.ID, p.CreatedAt, p.UpdatedAt, &p.CreatedBy, p.UpdatedBy),
		WorkspaceId: p.WorkspaceID.String(),
		Name:        p.Name,
		Identifier:  p.Identifier,
	}
	if p.Description != "" {
		pb.Description = &p.Description
	}
	if p.DefaultStateID != nil {
		s := p.DefaultStateID.String()
		pb.DefaultStateId = &s
	}
	return pb
}

// ── State ─────────────────────────────────────────────────────────────────────

func protoState(s *state.State) *v1.State {
	return &v1.State{
		Base:      baseFromFields(s.ID, s.CreatedAt, s.UpdatedAt, nil, nil),
		ProjectId: s.ProjectID.String(),
		Name:      s.Name,
		Group:     protoStateGroup(s.GroupName),
		Color:     s.Color,
		SortOrder: int32(s.SortOrder),
	}
}

// ── Label ─────────────────────────────────────────────────────────────────────

func protoLabel(l *label.Label) *v1.Label {
	pb := &v1.Label{
		Base:        baseFromFields(l.ID, l.CreatedAt, l.UpdatedAt, nil, nil),
		WorkspaceId: l.WorkspaceID.String(),
		Name:        l.Name,
		Color:       l.Color,
		SortOrder:   int32(l.SortOrder),
	}
	if l.ProjectID != nil {
		s := l.ProjectID.String()
		pb.ProjectId = &s
	}
	return pb
}

// ── Issue ─────────────────────────────────────────────────────────────────────

func protoIssue(i *issue.Issue) *v1.Issue {
	pb := &v1.Issue{
		Base:        baseFromDomain(i.Base),
		WorkspaceId: i.WorkspaceID.String(),
		ProjectId:   i.ProjectID.String(),
		Name:        i.Name,
		Priority:    protoPriority(i.Priority),
		AssigneeIds: uuidSlice(i.AssigneeIDs),
		LabelIds:    uuidSlice(i.LabelIDs),
		DueDate:     toTS(i.TargetDate),
	}
	if i.Description != "" {
		pb.Description = &i.Description
	}
	if i.StateID != nil {
		pb.StateId = i.StateID.String()
	}
	if i.EpicID != nil {
		s := i.EpicID.String()
		pb.EpicId = &s
	}
	if i.ParentID != nil {
		s := i.ParentID.String()
		pb.ParentId = &s
	}
	return pb
}

// ── Epic ──────────────────────────────────────────────────────────────────────

func protoEpic(e *epic.Epic) *v1.Epic {
	pb := &v1.Epic{
		Base:        baseFromDomain(e.Base),
		WorkspaceId: e.WorkspaceID.String(),
		ProjectId:   e.ProjectID.String(),
		Name:        e.Name,
		Priority:    protoPriority(e.Priority),
		AssigneeIds: uuidSlice(e.AssigneeIDs),
		LabelIds:    uuidSlice(e.LabelIDs),
	}
	if e.Description != "" {
		pb.Description = &e.Description
	}
	if e.StateID != nil {
		s := e.StateID.String()
		pb.StateId = &s
	}
	if e.ParentID != nil {
		s := e.ParentID.String()
		pb.ParentId = &s
	}
	return pb
}

// ── Cycle ─────────────────────────────────────────────────────────────────────

func protoCycle(c *cycle.Cycle) *v1.Cycle {
	pb := &v1.Cycle{
		Base:        baseFromFields(c.ID, c.CreatedAt, c.UpdatedAt, &c.CreatedBy, c.UpdatedBy),
		WorkspaceId: c.WorkspaceID.String(),
		ProjectId:   c.ProjectID.String(),
		Name:        c.Name,
		StartDate:   toTS(c.StartDate),
		EndDate:     toTS(c.EndDate),
	}
	if c.Description != "" {
		pb.Description = &c.Description
	}
	return pb
}

// ── Module ────────────────────────────────────────────────────────────────────

func protoModule(m *module.Module) *v1.Module {
	pb := &v1.Module{
		Base:        baseFromFields(m.ID, m.CreatedAt, m.UpdatedAt, &m.CreatedBy, m.UpdatedBy),
		WorkspaceId: m.WorkspaceID.String(),
		ProjectId:   m.ProjectID.String(),
		Name:        m.Name,
		StartDate:   toTS(m.StartDate),
		TargetDate:  toTS(m.TargetDate),
	}
	if m.Description != "" {
		pb.Description = &m.Description
	}
	if m.Status != "" {
		pb.Status = &m.Status
	}
	return pb
}
