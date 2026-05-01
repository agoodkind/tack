package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
)

// renderCtx carries the dependencies a markdown renderer needs to resolve
// cross-references (parent, scope, state, assignees, audit fields). Build
// one per tool call; the cache is in-flight and never shared across calls.
type renderCtx struct {
	ctx       context.Context
	reader    node.NodeReader
	users     user.Repository
	typeIndex map[string]*node.NodeType
	cache     map[uuid.UUID]string
}

func newRenderCtx(ctx context.Context, reader node.NodeReader, users user.Repository) *renderCtx {
	return &renderCtx{ctx: ctx, reader: reader, users: users, cache: map[uuid.UUID]string{}}
}

func newRenderCtxWithTypes(ctx context.Context, reader node.NodeReader, users user.Repository, typeIndex map[string]*node.NodeType) *renderCtx {
	return &renderCtx{ctx: ctx, reader: reader, users: users, typeIndex: typeIndex, cache: map[uuid.UUID]string{}}
}

// nodeIdentifier returns the human-readable identifier for a node UUID.
//
//   - For nodes with Props["identifier"] (projects), returns the bare
//     identifier (e.g. "TACK").
//   - For sequence-bearing nodes (issues, epics, cycles, modules), returns
//     "<SCOPE_IDENT>-<sequence>" (e.g. "TACK-65"). The scope is found via
//     Props["scope_id"]; stale or cross-org references fall back to the slug
//     or the bare sequence.
//   - For nodes with Props["slug"] (workspaces, orgs), returns the slug.
//   - Falls back to the node name. Returns "" only when the node cannot be
//     resolved at all.
//
// Cached per renderCtx so a 50-issue list does not re-resolve a shared scope
// 50 times.
func (rc *renderCtx) nodeIdentifier(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	if cached, ok := rc.cache[id]; ok {
		return cached
	}
	v, err := rc.reader.Get(rc.ctx, id)
	if err != nil || v == nil {
		rc.cache[id] = ""
		return ""
	}
	out := identifierFor(v, rc)
	rc.cache[id] = out
	return out
}

// identifierFor computes the identifier for a NodeView. Pulled out so single-
// node handlers can render their own view without a second Get round-trip.
func identifierFor(v *node.NodeView, rc *renderCtx) string {
	if v == nil {
		return ""
	}
	if rc != nil {
		if ident := rc.referenceIdentifierFor(v); ident != "" {
			return ident
		}
	}
	if ident := stringProp(v, "identifier"); ident != "" {
		return ident
	}
	if slug := stringProp(v, "slug"); slug != "" {
		return slug
	}
	return v.Name
}

// userDisplay returns the username-ish string for a user UUID, or the empty
// string when the lookup fails. Cached per renderCtx alongside node IDs.
func (rc *renderCtx) userDisplay(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	if cached, ok := rc.cache[id]; ok {
		return cached
	}
	if rc.users == nil {
		rc.cache[id] = ""
		return ""
	}
	u, err := rc.users.GetByID(rc.ctx, id)
	if err != nil || u == nil {
		rc.cache[id] = ""
		return ""
	}
	out := u.DisplayName
	if out == "" {
		out = u.Email
	}
	rc.cache[id] = out
	return out
}

// renderNode formats a single NodeView as a labeled markdown block. Cross-
// references resolve to their identifiers; the raw UUID survives at the
// bottom for round-tripping.
func renderNode(rc *renderCtx, v *node.NodeView) string {
	if v == nil {
		return "(node not found)"
	}
	var b strings.Builder
	ident := identifierFor(v, rc)
	header := v.Name
	if ident != "" && ident != v.Name {
		if strings.HasSuffix(ident, scopedNodeRefSeparator+v.Name) {
			header = ident
		} else {
			header = ident + "  " + v.Name
		}
	}
	fmt.Fprintf(&b, "%s\n\n", header)

	// Cross-reference fields (resolved to identifiers).
	writeRefField(&b, rc, "scope", uuidProp(v, "scope_id"))
	writeRefField(&b, rc, "parent", uuidProp(v, "parent_id"))
	writeRefField(&b, rc, "state", uuidProp(v, "state_id"))

	// Scalar Props.
	for _, key := range sortedScalarPropKeys(v) {
		val := propAsString(v.Props[key])
		if val == "" {
			continue
		}
		fmt.Fprintf(&b, "%-12s %s\n", key+":", val)
	}

	// Assignees as resolved user list.
	if assignees := uuidArrayProp(v, "assignee_ids"); len(assignees) > 0 {
		names := make([]string, 0, len(assignees))
		for _, uid := range assignees {
			if name := rc.userDisplay(uid); name != "" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "%-12s %s\n", "assignees:", strings.Join(names, ", "))
		}
	}

	// Audit fields.
	fmt.Fprintf(&b, "\n")
	writeUserField(&b, rc, "created", v.CreatedAt, v.CreatedBy)
	writeUserField(&b, rc, "updated", v.UpdatedAt, v.UpdatedBy)
	fmt.Fprintf(&b, "%-12s %s\n", "id:", v.ID)
	return b.String()
}

// renderList formats a list of NodeViews as a markdown-ish table. The
// columns are selected per the universal-fields-only rule: identifier,
// node_type, state (if any indexed), priority (if any), assignee count,
// and name.
//
// kind is the display label for the heading (e.g. "issues", "members").
func renderList(rc *renderCtx, kind string, vs []*node.NodeView) string {
	if len(vs) == 0 {
		return fmt.Sprintf("0 %s\n", kind)
	}
	rows := make([][]string, 0, len(vs)+1)
	header := []string{"identifier", "type", "state", "priority", "assigned", "name"}
	rows = append(rows, header)
	for _, v := range vs {
		ident := identifierFor(v, rc)
		state := rc.nodeIdentifier(uuidProp(v, "state_id"))
		priority := stringProp(v, "priority")
		assignees := uuidArrayProp(v, "assignee_ids")
		var assigned string
		switch {
		case len(assignees) == 0:
			assigned = "-"
		case len(assignees) == 1:
			assigned = rc.userDisplay(assignees[0])
		default:
			assigned = fmt.Sprintf("%d users", len(assignees))
		}
		rows = append(rows, []string{
			emptyDash(ident), emptyDash(v.NodeType), emptyDash(state), emptyDash(priority), emptyDash(assigned), v.Name,
		})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s\n", len(vs), kind)
	writeTable(&b, rows)
	return b.String()
}

// writeRefField writes one "label: identifier (type)" line when the ref
// resolves. Missing or zero refs print nothing so the output stays scannable.
func writeRefField(b *strings.Builder, rc *renderCtx, label string, id uuid.UUID) {
	if id == uuid.Nil {
		return
	}
	v, err := rc.reader.Get(rc.ctx, id)
	if err != nil || v == nil {
		fmt.Fprintf(b, "%-12s (missing %s)\n", label+":", id)
		return
	}
	ident := identifierFor(v, rc)
	rc.cache[id] = ident
	if ident == "" {
		ident = v.Name
	}
	fmt.Fprintf(b, "%-12s %s (%s)\n", label+":", ident, v.NodeType)
}

func writeUserField(b *strings.Builder, rc *renderCtx, label string, t time.Time, by uuid.UUID) {
	name := rc.userDisplay(by)
	stamp := t.UTC().Format("2006-01-02 15:04:05Z")
	if name != "" {
		fmt.Fprintf(b, "%-12s %s by %s\n", label+":", stamp, name)
	} else {
		fmt.Fprintf(b, "%-12s %s\n", label+":", stamp)
	}
}

// writeTable lays out rows with column widths chosen from the longest cell
// in each column. Two-space gutters between columns.
func writeTable(b *strings.Builder, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	cols := len(rows[0])
	widths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for _, row := range rows {
		for i, cell := range row {
			if i == cols-1 {
				b.WriteString(cell)
			} else {
				fmt.Fprintf(b, "%-*s  ", widths[i], cell)
			}
		}
		b.WriteByte('\n')
	}
}

// --- typed prop accessors ---

func numberProp(v *node.NodeView, key string) int64 {
	raw, ok := v.Props[key]
	if !ok {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f)
	}
	return 0
}

func uuidProp(v *node.NodeView, key string) uuid.UUID {
	raw, ok := v.Props[key]
	if !ok {
		return uuid.Nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func uuidArrayProp(v *node.NodeView, key string) []uuid.UUID {
	raw, ok := v.Props[key]
	if !ok {
		return nil
	}
	var ss []string
	if err := json.Unmarshal(raw, &ss); err != nil {
		return nil
	}
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		if id, err := uuid.Parse(s); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// stringProp returns a string property, decoding standard JSON-encoded
// strings. Returns empty when the key is missing or not a string.
func stringProp(v *node.NodeView, key string) string {
	raw, ok := v.Props[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// propAsString gives a best-effort string rendering of a raw JSON value.
// Strings come back unquoted, numbers and booleans as their literal form,
// arrays and objects fall back to compact JSON.
func propAsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", n), "0"), ".")
	}
	var bv bool
	if err := json.Unmarshal(raw, &bv); err == nil {
		return fmt.Sprintf("%t", bv)
	}
	return string(raw)
}

// sortedScalarPropKeys returns the Props keys for which we want to render a
// scalar line. UUIDs we render via writeRefField are excluded; assignee_ids
// is rendered separately. Audit and structural keys (parent_id, scope_id,
// state_id) are owned by the cross-reference path. Identifier and sequence
// are folded into the header. Slug appears as a plain field.
func sortedScalarPropKeys(v *node.NodeView) []string {
	skip := map[string]struct{}{
		"parent_id":    {},
		"scope_id":     {},
		"state_id":     {},
		"assignee_ids": {},
		"identifier":   {},
		"sequence":     {},
	}
	keys := make([]string, 0, len(v.Props))
	for k := range v.Props {
		if _, hide := skip[k]; hide {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// renderWorkspaceDescribe builds a markdown description of a workspace,
// listing its node types and direct children. Uses the same labeled-block
// shape as renderNode for the workspace itself, then a section per area.
func renderWorkspaceDescribe(rc *renderCtx, ws *node.NodeView, types []nodeTypeSummary, children []*node.NodeView) string {
	var b strings.Builder
	b.WriteString(renderNode(rc, ws))
	if len(types) > 0 {
		b.WriteString("\nnode types\n")
		rows := [][]string{{"slug", "plural", "name", "features"}}
		for _, t := range types {
			rows = append(rows, []string{t.Slug, t.PluralSlug, t.Name, strings.Join(t.Features, ", ")})
		}
		writeTable(&b, rows)
	}
	if len(children) > 0 {
		b.WriteString("\ndirect children\n")
		b.WriteString(renderList(rc, "children", children))
	}
	return b.String()
}

// renderMembers formats org members as a labeled list. Members come back
// as user records, so the column layout mirrors the user identity model.
func renderMembers(users []*user.User) string {
	if len(users) == 0 {
		return "0 members\n"
	}
	rows := [][]string{{"display_name", "email"}}
	for _, u := range users {
		rows = append(rows, []string{u.DisplayName, u.Email})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d members\n", len(users))
	writeTable(&b, rows)
	return b.String()
}

// renderRelationships formats a list of relationships either outbound or
// inbound from a node. Each row shows direction, type, and the resolved
// other-side identifier.
func renderRelationships(rc *renderCtx, direction string, rels []*node.Relationship, otherIsTarget bool) string {
	if len(rels) == 0 {
		return fmt.Sprintf("0 relationships %s\n", direction)
	}
	rows := [][]string{{"relation", "other"}}
	for _, r := range rels {
		other := r.SourceID
		if otherIsTarget {
			other = r.TargetID
		}
		rows = append(rows, []string{r.RelationType, emptyDash(rc.nodeIdentifier(other))})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d relationships %s\n", len(rels), direction)
	writeTable(&b, rows)
	return b.String()
}

// renderSearch formats search docs as a table. NodeDoc lacks the rich Props
// map a NodeView has, so we render the columns the index actually carries:
// node_type, name, and parent if known.
func renderSearchHits(hits []searchHit) string {
	if len(hits) == 0 {
		return "0 results\n"
	}
	rows := [][]string{{"id", "type", "name"}}
	for _, h := range hits {
		rows = append(rows, []string{h.ID, h.NodeType, h.Name})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d results\n", len(hits))
	writeTable(&b, rows)
	return b.String()
}

// searchHit is the lean per-document shape the search renderer prints.
// Centralised here so the search tool's marshaling does not need to know
// the full domainsearch.NodeDoc layout.
type searchHit struct {
	ID       string
	NodeType string
	Name     string
}
