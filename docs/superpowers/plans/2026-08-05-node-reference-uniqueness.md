# Node Reference Uniqueness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Guarantee that a human-readable node reference such as `FAN-13` resolves to exactly one node, enforced by the storage layer at write time, with the uniqueness domain declared as data on the NodeType.

**Architecture:** A NodeType carries a list of reference key templates. A template is an ordered list of parts that renders one string. That single declaration renders the reference, parses it, forms the uniqueness key, and forms the counter key. Two FoundationDB key families hold the constraint: a forward family mapping a rendered key to a node identifier, and a reverse family mapping a node identifier to the keys it holds. Every write path checks the forward key inside its existing transaction. A value generator derives its counter key from the template minus the part it fills, so the counter domain and the uniqueness domain cannot diverge.

**Tech Stack:** Go, FoundationDB tuple layer, `github.com/google/uuid` (UUIDv7), the `internal/ops` maintenance registry, `make build` and `make test-integration`.

**Design of record:** `docs/superpowers/specs/2026-08-05-node-reference-uniqueness-design.md`.

## Global Constraints

- Build with `make build`. It runs vet, golangci, staticcheck-extra, and govulncheck, baseline-gated. Never call `go build` directly.
- Everything is a node. Behavior follows NodeType metadata, never hardcoded type names. No task may introduce a branch on a concrete type key such as `issue` or `epic`.
- The storage layer does not read PropertyDefs or NodeTypes. The service resolves metadata and passes the result down, exactly as it already does for `indexedProps` (`internal/domain/node/repository.go` lines 84 to 86).
- No file exceeds 200 lines. Split by concern when it does.
- Every write goes through `EntityRepository.CreateAtomic`, `Set`, or `UpdateAtomic`, each in one FoundationDB transaction. No multi-step creates, no cross-database transactions.
- Use `log/slog` with named attributes. Message names use `noun.verb`. `Info` for normal flow, `Debug` for trace detail, `Error` for actual failures.
- Every error wraps context and the relevant identifier: `fmt.Errorf("get issue %s: %w", id, err)`.
- Use the full domain term, not abbreviations. Write `workspaceID`, not `wsID`.
- Every package has a `// Package X` doc comment. Every exported type and non-obvious field has a doc comment.
- No backwards-compatibility shims, unused exports, or re-exports.
- Do not add docstrings or comments to code you did not change.
- Integration tests build-tag `integration` and skip when their DSN environment variable is unset, matching `internal/audit/chain_append_integration_test.go`.
- Maintenance operations register through `ops.Register` in `internal/ops`. Operation names are lowercase and dot-separated.
- Commit with `git commit -S`. Subject line in imperative mood, no trailing period, followed by a blank line and `Co-authored-by: Claude <noreply@anthropic.com>`.
- Validate every migration, seed, backfill, and restore on QA before production. QA is disposable.

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/domain/node/reference_template.go` | Create. Template and part types, `Render`, `Parse`, `CounterKey`. Pure domain, no storage. |
| `internal/domain/node/reference_template_test.go` | Create. Unit tests for render, parse, and counter derivation. |
| `internal/domain/node/types.go` | Modify. Add `ReferenceTemplates` to `NodeType` and the `PrimaryReferenceTemplate` accessor. |
| `internal/domain/node/repository.go` | Modify. Add `ReferenceKey`, extend `CreateAtomic` and `UpdateAtomic`, add `LookupReference` and `AllocateSequenceByKey`. |
| `internal/adapters/foundationdb/keys.go` | Modify. Add the two reference key families and their packers. |
| `internal/adapters/foundationdb/node_reference.go` | Create. In-transaction reference key write, clear, and lookup helpers. |
| `internal/adapters/foundationdb/node.go` | Modify. Call the reference helpers from `CreateAtomic`, `UpdateAtomic`, `Set`, and `Delete`. Add `AllocateSequenceByKey`. |
| `internal/service/node_reference_keys.go` | Create. Render a node's reference keys and counter key from NodeType metadata. |
| `internal/service/node_create_prepare.go` | Modify. Allocate into the template's generated property using the derived counter key. |
| `internal/service/node_create.go` | Modify. Pass rendered reference keys to `createNodeAtomic`. |
| `internal/service/seed.go` | Modify. Seed reference templates on the built-in types. |
| `internal/adapters/mcp/tools/resolve_typed.go` | Modify. Point-read resolution with a fail-loud scan fallback. |
| `internal/ops/reference_duplicates.go` | Create. The `reference.duplicates` read operation. |
| `internal/ops/repair_reference_uniqueness.go` | Create. The `repair.reference_uniqueness` renumber and backfill operation. |
| `internal/test/integration/reference_uniqueness_test.go` | Create. Both reproductions and the post-repair assertions. |

---

### Task 1: Reference template domain types, render, parse, and counter derivation

**Files:**
- Create: `internal/domain/node/reference_template.go`
- Create: `internal/domain/node/reference_template_test.go`
- Modify: `internal/domain/node/types.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `node.ReferencePartKind` (string), constants `ReferencePartScopeRef`, `ReferencePartProperty`, `ReferencePartNodeType`, `ReferencePartLiteral`; `node.ReferencePart{Kind ReferencePartKind, Value string}`; `node.ReferenceTemplate{Name string, Parts []ReferencePart, IsPrimary bool, Generated string}`; `node.ReferenceRenderInput{NodeTypeKey string, Props map[string]json.RawMessage, ScopeRefs map[string]string}`; `node.ReferenceParseResult{ScopeRefs map[string]string, Props map[string]string, NodeTypeKey string}`; methods `ReferenceTemplate.Render(ReferenceRenderInput) (string, error)`, `ReferenceTemplate.Parse(string) (ReferenceParseResult, error)`, `ReferenceTemplate.CounterKey(ReferenceRenderInput) (string, error)`; `NodeType.ReferenceTemplates []ReferenceTemplate` and `NodeType.PrimaryReferenceTemplate() *ReferenceTemplate`.

- [ ] **Step 1: Write the failing test**

Create `internal/domain/node/reference_template_test.go`:

```go
package node

import (
	"encoding/json"
	"testing"
)

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return b
}

// issueTemplate is the shape seeded for work-item types: the enclosing scope's
// reference, a hyphen, then the generated sequence property. The node type is
// deliberately absent, so an epic and an issue cannot both hold "FAN-1".
func issueTemplate() ReferenceTemplate {
	return ReferenceTemplate{
		Name:      "reference",
		IsPrimary: true,
		Generated: "sequence",
		Parts: []ReferencePart{
			{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
			{Kind: ReferencePartLiteral, Value: "-"},
			{Kind: ReferencePartProperty, Value: "sequence"},
		},
	}
}

func TestReferenceTemplateRendersScopedSequence(t *testing.T) {
	got, err := issueTemplate().Render(ReferenceRenderInput{
		NodeTypeKey: "issue",
		Props:       map[string]json.RawMessage{"sequence": rawJSON(t, 13)},
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "FAN-13" {
		t.Fatalf("Render = %q, want %q", got, "FAN-13")
	}
}

func TestReferenceTemplateRendersScopedProperty(t *testing.T) {
	template := ReferenceTemplate{
		Name:      "reference",
		IsPrimary: true,
		Parts: []ReferencePart{
			{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
			{Kind: ReferencePartLiteral, Value: "::"},
			{Kind: ReferencePartProperty, Value: "name"},
		},
	}
	got, err := template.Render(ReferenceRenderInput{
		NodeTypeKey: "state",
		Props:       map[string]json.RawMessage{"name": rawJSON(t, "In Progress")},
		ScopeRefs:   map[string]string{FeatureIsScope: "CLYDE"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "CLYDE::In Progress" {
		t.Fatalf("Render = %q, want %q", got, "CLYDE::In Progress")
	}
}

func TestReferenceTemplateRendersNodeTypePart(t *testing.T) {
	template := ReferenceTemplate{
		Name:      "reference",
		IsPrimary: true,
		Generated: "sequence",
		Parts: []ReferencePart{
			{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
			{Kind: ReferencePartLiteral, Value: "-"},
			{Kind: ReferencePartNodeType},
			{Kind: ReferencePartLiteral, Value: "-"},
			{Kind: ReferencePartProperty, Value: "sequence"},
		},
	}
	got, err := template.Render(ReferenceRenderInput{
		NodeTypeKey: "epic",
		Props:       map[string]json.RawMessage{"sequence": rawJSON(t, 1)},
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "FAN-epic-1" {
		t.Fatalf("Render = %q, want %q", got, "FAN-epic-1")
	}
}

func TestReferenceTemplateRenderMissingScopeRefFails(t *testing.T) {
	_, err := issueTemplate().Render(ReferenceRenderInput{
		NodeTypeKey: "issue",
		Props:       map[string]json.RawMessage{"sequence": rawJSON(t, 13)},
		ScopeRefs:   map[string]string{},
	})
	if err == nil {
		t.Fatal("Render with no scope reference: got nil error, want failure")
	}
}

func TestReferenceTemplateRenderMissingPropertyFails(t *testing.T) {
	_, err := issueTemplate().Render(ReferenceRenderInput{
		NodeTypeKey: "issue",
		Props:       map[string]json.RawMessage{},
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err == nil {
		t.Fatal("Render with no sequence property: got nil error, want failure")
	}
}

func TestReferenceTemplateParsesScopedSequence(t *testing.T) {
	got, err := issueTemplate().Parse("FAN-13")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ScopeRefs[FeatureIsScope] != "FAN" {
		t.Fatalf("scope reference = %q, want %q", got.ScopeRefs[FeatureIsScope], "FAN")
	}
	if got.Props["sequence"] != "13" {
		t.Fatalf("sequence = %q, want %q", got.Props["sequence"], "13")
	}
}

// TestReferenceTemplateParsesHyphenatedScope covers a scope reference that
// itself contains the separator. The trailing property consumes only the final
// segment, so the scope keeps its hyphen.
func TestReferenceTemplateParsesHyphenatedScope(t *testing.T) {
	got, err := issueTemplate().Parse("FAN-QA-13")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.ScopeRefs[FeatureIsScope] != "FAN-QA" {
		t.Fatalf("scope reference = %q, want %q", got.ScopeRefs[FeatureIsScope], "FAN-QA")
	}
	if got.Props["sequence"] != "13" {
		t.Fatalf("sequence = %q, want %q", got.Props["sequence"], "13")
	}
}

func TestReferenceTemplateParseRejectsMissingSeparator(t *testing.T) {
	if _, err := issueTemplate().Parse("FAN13"); err == nil {
		t.Fatal("Parse without a separator: got nil error, want failure")
	}
}

// TestReferenceTemplateCounterKeyExcludesGenerated pins the derivation rule: the
// counter key is the template minus the part the generator fills. The node type
// is absent from this template, so it is absent from the counter key, which is
// what stops an epic and an issue sharing a number.
func TestReferenceTemplateCounterKeyExcludesGenerated(t *testing.T) {
	got, err := issueTemplate().CounterKey(ReferenceRenderInput{
		NodeTypeKey: "issue",
		Props:       map[string]json.RawMessage{},
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err != nil {
		t.Fatalf("CounterKey: %v", err)
	}
	if got != "FAN-" {
		t.Fatalf("CounterKey = %q, want %q", got, "FAN-")
	}
}

func TestReferenceTemplateCounterKeyIncludesNodeTypeWhenDeclared(t *testing.T) {
	template := ReferenceTemplate{
		Name:      "reference",
		Generated: "sequence",
		Parts: []ReferencePart{
			{Kind: ReferencePartScopeRef, Value: FeatureIsScope},
			{Kind: ReferencePartLiteral, Value: "-"},
			{Kind: ReferencePartNodeType},
			{Kind: ReferencePartLiteral, Value: "-"},
			{Kind: ReferencePartProperty, Value: "sequence"},
		},
	}
	issueKey, err := template.CounterKey(ReferenceRenderInput{
		NodeTypeKey: "issue",
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err != nil {
		t.Fatalf("CounterKey(issue): %v", err)
	}
	epicKey, err := template.CounterKey(ReferenceRenderInput{
		NodeTypeKey: "epic",
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err != nil {
		t.Fatalf("CounterKey(epic): %v", err)
	}
	if issueKey == epicKey {
		t.Fatalf("counter keys for issue and epic both %q, want different keys", issueKey)
	}
}

func TestPrimaryReferenceTemplateSelectsTheMarkedOne(t *testing.T) {
	nodeType := &NodeType{ReferenceTemplates: []ReferenceTemplate{
		{Name: "external_id", Parts: []ReferencePart{{Kind: ReferencePartProperty, Value: "external_id"}}},
		issueTemplate(),
	}}
	primary := nodeType.PrimaryReferenceTemplate()
	if primary == nil {
		t.Fatal("PrimaryReferenceTemplate = nil, want the template marked primary")
	}
	if primary.Name != "reference" {
		t.Fatalf("primary template name = %q, want %q", primary.Name, "reference")
	}
}

func TestPrimaryReferenceTemplateNilWhenNoneDeclared(t *testing.T) {
	if got := (&NodeType{}).PrimaryReferenceTemplate(); got != nil {
		t.Fatalf("PrimaryReferenceTemplate on a type with no templates = %+v, want nil", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/domain/node/ -run 'TestReferenceTemplate|TestPrimaryReferenceTemplate' -v`

Expected: FAIL to compile, with `undefined: ReferenceTemplate`.

- [ ] **Step 3: Write the domain types**

Create `internal/domain/node/reference_template.go`:

```go
package node

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ReferencePartKind names one component of a reference key template.
type ReferencePartKind string

const (
	// ReferencePartScopeRef renders the rendered reference of the nearest
	// ancestor declaring the feature named in ReferencePart.Value.
	ReferencePartScopeRef ReferencePartKind = "scope_ref"
	// ReferencePartProperty renders the node property named in ReferencePart.Value.
	ReferencePartProperty ReferencePartKind = "property"
	// ReferencePartNodeType renders the node's own TypeKey. Value is unused.
	ReferencePartNodeType ReferencePartKind = "node_type"
	// ReferencePartLiteral renders ReferencePart.Value verbatim, and acts as a
	// delimiter when parsing.
	ReferencePartLiteral ReferencePartKind = "literal"
)

// ReferencePart is one component of a reference key template.
type ReferencePart struct {
	Kind ReferencePartKind `json:"kind"`
	// Value carries the feature name, property name, or literal text the Kind
	// requires. ReferencePartNodeType ignores it.
	Value string `json:"value,omitempty"`
}

// ReferenceTemplate declares a uniqueness constraint over the string its Parts
// render. Two nodes in one org may not render the same string for the same
// template. The template marked IsPrimary is also the human-readable reference
// for its NodeType.
type ReferenceTemplate struct {
	// Name identifies the template within its NodeType and appears in the
	// storage key and in conflict errors.
	Name string `json:"name"`
	// Parts render in order.
	Parts []ReferencePart `json:"parts"`
	// IsPrimary marks the template that renders the human-readable reference.
	// At most one template per NodeType sets it.
	IsPrimary bool `json:"is_primary,omitempty"`
	// Generated names the property a value generator fills at create time.
	// Empty means the caller supplies every part.
	Generated string `json:"generated,omitempty"`
}

// ReferenceRenderInput carries everything a template needs to render. ScopeRefs
// maps a feature name to the rendered reference of the nearest ancestor
// declaring that feature.
type ReferenceRenderInput struct {
	NodeTypeKey string
	Props       map[string]json.RawMessage
	ScopeRefs   map[string]string
}

// ReferenceParseResult holds the values recovered from a rendered reference.
type ReferenceParseResult struct {
	ScopeRefs   map[string]string
	Props       map[string]string
	NodeTypeKey string
}

// Render produces the template's string. It fails when a part has no value,
// because a partially rendered reference would silently collide with another.
func (t ReferenceTemplate) Render(in ReferenceRenderInput) (string, error) {
	var out strings.Builder
	for _, part := range t.Parts {
		segment, err := renderReferencePart(part, in)
		if err != nil {
			return "", fmt.Errorf("render template %q: %w", t.Name, err)
		}
		out.WriteString(segment)
	}
	return out.String(), nil
}

// CounterKey produces the template's string with the generated part omitted. A
// value generator keys its counter on this, so the counter domain always equals
// the uniqueness domain.
func (t ReferenceTemplate) CounterKey(in ReferenceRenderInput) (string, error) {
	var out strings.Builder
	for _, part := range t.Parts {
		if part.Kind == ReferencePartProperty && part.Value == t.Generated {
			continue
		}
		segment, err := renderReferencePart(part, in)
		if err != nil {
			return "", fmt.Errorf("counter key for template %q: %w", t.Name, err)
		}
		out.WriteString(segment)
	}
	return out.String(), nil
}

func renderReferencePart(part ReferencePart, in ReferenceRenderInput) (string, error) {
	switch part.Kind {
	case ReferencePartLiteral:
		return part.Value, nil
	case ReferencePartNodeType:
		if in.NodeTypeKey == "" {
			return "", fmt.Errorf("node type part: node type key is empty")
		}
		return in.NodeTypeKey, nil
	case ReferencePartScopeRef:
		value := in.ScopeRefs[part.Value]
		if value == "" {
			return "", fmt.Errorf("scope reference part %q: no ancestor declaring that feature has a reference", part.Value)
		}
		return value, nil
	case ReferencePartProperty:
		value, err := referencePropertyString(in.Props[part.Value])
		if err != nil {
			return "", fmt.Errorf("property part %q: %w", part.Value, err)
		}
		return value, nil
	default:
		return "", fmt.Errorf("unknown reference part kind %q", part.Kind)
	}
}

// referencePropertyString renders a JSON property value as reference text. A
// number renders without a decimal point so sequence 13 becomes "13".
func referencePropertyString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("property has no value")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return "", fmt.Errorf("property is an empty string")
		}
		return text, nil
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return strconv.FormatFloat(number, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("property value %s is neither a string nor a number", raw)
}

// Parse recovers the variable parts of a rendered reference. Literal parts act
// as delimiters. A variable part consumes text up to the next literal, except
// the final variable part, which consumes the remainder. A leading variable part
// therefore keeps any separators the following literal also uses, so a scope
// reference containing a hyphen survives a hyphen delimiter.
func (t ReferenceTemplate) Parse(input string) (ReferenceParseResult, error) {
	result := ReferenceParseResult{
		ScopeRefs: map[string]string{},
		Props:     map[string]string{},
	}
	remaining := input
	for index, part := range t.Parts {
		if part.Kind == ReferencePartLiteral {
			continue
		}
		segment, rest, err := consumeReferenceSegment(t.Parts, index, remaining)
		if err != nil {
			return ReferenceParseResult{}, fmt.Errorf("parse %q against template %q: %w", input, t.Name, err)
		}
		remaining = rest
		switch part.Kind {
		case ReferencePartScopeRef:
			result.ScopeRefs[part.Value] = segment
		case ReferencePartProperty:
			result.Props[part.Value] = segment
		case ReferencePartNodeType:
			result.NodeTypeKey = segment
		case ReferencePartLiteral:
		}
	}
	return result, nil
}

// consumeReferenceSegment takes the text belonging to the variable part at
// index, and returns it with the text left after the following literal.
func consumeReferenceSegment(parts []ReferencePart, index int, remaining string) (string, string, error) {
	delimiter := nextReferenceLiteral(parts, index)
	if delimiter == "" {
		if remaining == "" {
			return "", "", fmt.Errorf("no text left for the final part")
		}
		return remaining, "", nil
	}
	cut := strings.LastIndex(remaining, delimiter)
	if cut < 0 {
		return "", "", fmt.Errorf("separator %q not found", delimiter)
	}
	if cut == 0 {
		return "", "", fmt.Errorf("no text before separator %q", delimiter)
	}
	return remaining[:cut], remaining[cut+len(delimiter):], nil
}

func nextReferenceLiteral(parts []ReferencePart, index int) string {
	for cursor := index + 1; cursor < len(parts); cursor++ {
		if parts[cursor].Kind == ReferencePartLiteral {
			return parts[cursor].Value
		}
	}
	return ""
}
```

- [ ] **Step 4: Add the NodeType field and accessor**

In `internal/domain/node/types.go`, add to the `NodeType` struct immediately after the `Reference` field (line 146):

```go
	// ReferenceTemplates declares uniqueness constraints for this type. Two
	// nodes in one org may not render the same string for the same template.
	// At most one template sets IsPrimary, which makes it the human-readable
	// reference.
	ReferenceTemplates []ReferenceTemplate `json:"reference_templates,omitempty"`
```

Add below `BuildTypeIndex` in the same file:

```go
// PrimaryReferenceTemplate returns the template that renders this type's
// human-readable reference, or nil when the type declares none.
func (nt *NodeType) PrimaryReferenceTemplate() *ReferenceTemplate {
	for index := range nt.ReferenceTemplates {
		if nt.ReferenceTemplates[index].IsPrimary {
			return &nt.ReferenceTemplates[index]
		}
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/domain/node/ -run 'TestReferenceTemplate|TestPrimaryReferenceTemplate' -v`

Expected: PASS, all eleven tests.

- [ ] **Step 6: Build**

Run: `make build`

Expected: success.

- [ ] **Step 7: Commit**

```bash
git add internal/domain/node/reference_template.go internal/domain/node/reference_template_test.go internal/domain/node/types.go
git commit -S -m "Add reference key templates to NodeType metadata (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 2: Reference key families and in-transaction storage helpers

**Files:**
- Modify: `internal/adapters/foundationdb/keys.go`
- Create: `internal/adapters/foundationdb/node_reference.go`
- Modify: `internal/domain/node/repository.go`
- Test: `internal/test/integration/reference_store_test.go`

**Interfaces:**
- Consumes: `node.ReferenceTemplate` (Task 1), though this task uses only rendered strings.
- Produces: key packers `nodeReferenceKey(orgID uuid.UUID, templateName, encoded string) []byte`, `nodeReferenceOwnedKey(orgID, nodeID uuid.UUID, templateName string) []byte`, `nodeReferenceOwnedPrefix(orgID, nodeID uuid.UUID) []byte`; in-transaction helpers `writeReferenceKeys(tr fdb.Transaction, orgID, nodeID uuid.UUID, keys []node.ReferenceKey) error` and `clearReferenceKeys(tr fdb.Transaction, orgID, nodeID uuid.UUID) error`; store method `(*NodeStore).LookupReference(ctx context.Context, orgID uuid.UUID, templateName, encoded string) (uuid.UUID, error)`; domain type `node.ReferenceKey{TemplateName string, Encoded string}`.

- [ ] **Step 1: Write the failing test**

Create `internal/test/integration/reference_store_test.go`:

```go
package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// TestReferenceKeyRejectsASecondHolder is the core enforcement assertion: two
// different nodes cannot hold the same rendered reference for one template.
func TestReferenceKeyRejectsASecondHolder(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)

	first := mustCreate(t, env, service.CreateInput{
		ParentID:    project.ID,
		ScopeID:     project.ID,
		NodeTypeKey: "issue",
		Name:        "First",
		ActorID:     actor,
	})
	keys := []node.ReferenceKey{{TemplateName: "reference", Encoded: "FAN-1"}}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, first.ID, keys); err != nil {
		t.Fatalf("SetReferenceKeys on the first node: %v", err)
	}

	second := mustCreate(t, env, service.CreateInput{
		ParentID:    project.ID,
		ScopeID:     project.ID,
		NodeTypeKey: "issue",
		Name:        "Second",
		ActorID:     actor,
	})
	err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, second.ID, keys)
	if err == nil {
		t.Fatal("SetReferenceKeys on a second node holding the same reference: got nil error, want conflict")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("SetReferenceKeys error = %v, want a domain.ErrConflict", err)
	}
}

// TestReferenceKeyIsIdempotentForItsHolder confirms re-writing the same key for
// the same node succeeds, so an update that does not change the reference is a
// no-op rather than a self-conflict.
func TestReferenceKeyIsIdempotentForItsHolder(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	issue := mustCreate(t, env, service.CreateInput{
		ParentID:    project.ID,
		ScopeID:     project.ID,
		NodeTypeKey: "issue",
		Name:        "Only",
		ActorID:     actor,
	})

	keys := []node.ReferenceKey{{TemplateName: "reference", Encoded: "FAN-7"}}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, issue.ID, keys); err != nil {
		t.Fatalf("first SetReferenceKeys: %v", err)
	}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, issue.ID, keys); err != nil {
		t.Fatalf("second SetReferenceKeys for the same node: %v", err)
	}
}

// TestReferenceKeyMovesWithTheNode confirms the reverse index lets a node give
// up its prior reference, so the prior value becomes available again.
func TestReferenceKeyMovesWithTheNode(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	mover := mustCreate(t, env, service.CreateInput{
		ParentID:    project.ID,
		ScopeID:     project.ID,
		NodeTypeKey: "issue",
		Name:        "Mover",
		ActorID:     actor,
	})
	taker := mustCreate(t, env, service.CreateInput{
		ParentID:    project.ID,
		ScopeID:     project.ID,
		NodeTypeKey: "issue",
		Name:        "Taker",
		ActorID:     actor,
	})

	original := []node.ReferenceKey{{TemplateName: "reference", Encoded: "FAN-9"}}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, mover.ID, original); err != nil {
		t.Fatalf("claim FAN-9: %v", err)
	}
	replacement := []node.ReferenceKey{{TemplateName: "reference", Encoded: "FAN-10"}}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, mover.ID, replacement); err != nil {
		t.Fatalf("move to FAN-10: %v", err)
	}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, taker.ID, original); err != nil {
		t.Fatalf("claim the released FAN-9: %v", err)
	}

	holder, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "FAN-9")
	if err != nil {
		t.Fatalf("LookupReference(FAN-9): %v", err)
	}
	if holder != taker.ID {
		t.Fatalf("FAN-9 holder = %s, want the taker %s", holder, taker.ID)
	}
}

// TestLookupReferenceMissReturnsNil confirms an unheld reference resolves to the
// nil UUID with no error, so callers can fall back rather than treating a miss
// as a failure.
func TestLookupReferenceMissReturnsNil(t *testing.T) {
	env := SetupTestEnv(t)
	holder, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "FAN-404")
	if err != nil {
		t.Fatalf("LookupReference on a miss: %v", err)
	}
	if holder != uuid.Nil {
		t.Fatalf("LookupReference on a miss = %s, want uuid.Nil", holder)
	}
}
```

Add `"errors"` and `"goodkind.io/tack/internal/service"` to the import block.

- [ ] **Step 2: Run the test to verify it fails**

Run: `make test-integration`

Expected: FAIL to compile, with `env.Stores.Nodes.SetReferenceKeys undefined`.

- [ ] **Step 3: Add the key families**

In `internal/adapters/foundationdb/keys.go`, add to the `const` block after `keyIdempotency`:

```go
	// Forward reference uniqueness index. One entry per constrained value; the
	// value is the holding node's UUID string. A write that finds a different
	// holder is a conflict.
	// (node_reference, orgID, templateName, encodedKey) -> nodeID
	keyNodeReference = "node_reference"

	// Reverse index from a node to the reference keys it holds. Delete and
	// UpdateAtomic need a node's prior keys without NodeType metadata, which
	// the storage layer never reads.
	// (node_reference_owned, orgID, nodeID, templateName) -> encodedKey
	keyNodeReferenceOwned = "node_reference_owned"
```

Add the packers after `idempotencyKey`:

```go
// nodeReferenceKey packs the forward reference uniqueness key.
func nodeReferenceKey(orgID uuid.UUID, templateName, encoded string) []byte {
	return withPrefix(tuple.Tuple{keyNodeReference, orgID.String(), templateName, encoded}.Pack())
}

// nodeReferenceOwnedKey packs the reverse key naming one reference a node holds.
func nodeReferenceOwnedKey(orgID, nodeID uuid.UUID, templateName string) []byte {
	return withPrefix(tuple.Tuple{keyNodeReferenceOwned, orgID.String(), nodeID.String(), templateName}.Pack())
}

// nodeReferenceOwnedPrefix packs the prefix covering every reference a node holds.
func nodeReferenceOwnedPrefix(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeReferenceOwned, orgID.String(), nodeID.String()}.Pack())
}
```

- [ ] **Step 4: Add the domain type and repository methods**

In `internal/domain/node/repository.go`, add above the `NodeRepository` interface:

```go
// ReferenceKey pairs a NodeType reference template with the string that
// template rendered for one node. The service renders these; the storage layer
// stores them without interpreting them.
type ReferenceKey struct {
	// TemplateName is ReferenceTemplate.Name.
	TemplateName string
	// Encoded is the rendered reference, for example "FAN-13".
	Encoded string
}
```

Add to the `NodeRepository` interface, after `AllocateSequence`:

```go
	// SetReferenceKeys replaces every reference key the node holds with keys.
	// Returns a domain.ErrConflict when another node already holds one of them.
	// Passing an empty slice releases every key the node holds.
	SetReferenceKeys(ctx context.Context, orgID, nodeID uuid.UUID, keys []ReferenceKey) error

	// LookupReference returns the node holding encoded under templateName, or
	// uuid.Nil with a nil error when no node holds it.
	LookupReference(ctx context.Context, orgID uuid.UUID, templateName, encoded string) (uuid.UUID, error)

	// AllocateSequenceByKey atomically increments and returns the counter named
	// by counterKey, which the caller derives from a reference template minus
	// the generated part.
	AllocateSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string) (int64, error)

	// SeedSequenceByKey raises the counter named by counterKey to at least
	// value. It never lowers a counter. The repair backfill uses it to carry a
	// scope's high-water mark onto a newly derived counter key.
	SeedSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string, value int64) error
```

- [ ] **Step 5: Write the storage helpers**

Create `internal/adapters/foundationdb/node_reference.go`:

```go
package foundationdb

import (
	"context"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// writeReferenceKeys releases every reference key the node currently holds and
// claims the given ones, inside the caller's transaction. A key held by another
// node fails the transaction, which is the uniqueness guarantee.
func writeReferenceKeys(tr fdb.Transaction, orgID, nodeID uuid.UUID, keys []node.ReferenceKey) error {
	if err := clearReferenceKeys(tr, orgID, nodeID); err != nil {
		return err
	}
	for _, key := range keys {
		forward := fdb.Key(nodeReferenceKey(orgID, key.TemplateName, key.Encoded))
		existing, err := tr.Get(forward).Get()
		if err != nil {
			return fmt.Errorf("read reference key %q for template %q: %w", key.Encoded, key.TemplateName, err)
		}
		if len(existing) > 0 && string(existing) != nodeID.String() {
			return fmt.Errorf(
				"reference %q for template %q is already held by node %s: %w",
				key.Encoded, key.TemplateName, string(existing), domain.ErrConflict,
			)
		}
		tr.Set(forward, []byte(nodeID.String()))
		tr.Set(fdb.Key(nodeReferenceOwnedKey(orgID, nodeID, key.TemplateName)), []byte(key.Encoded))
	}
	return nil
}

// clearReferenceKeys releases every reference key the node holds, inside the
// caller's transaction. It reads the reverse index, so it needs no NodeType
// metadata.
func clearReferenceKeys(tr fdb.Transaction, orgID, nodeID uuid.UUID) error {
	ownedRange, err := fdb.PrefixRange(nodeReferenceOwnedPrefix(orgID, nodeID))
	if err != nil {
		return fmt.Errorf("reference reverse range for node %s: %w", nodeID, err)
	}
	owned, err := tr.GetRange(ownedRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return fmt.Errorf("read reference reverse index for node %s: %w", nodeID, err)
	}
	for _, kv := range owned {
		unpacked, unpackErr := tuple.Unpack(stripPrefix(kv.Key))
		if unpackErr != nil || len(unpacked) < 4 {
			continue
		}
		templateName, _ := unpacked[3].(string)
		tr.Clear(fdb.Key(nodeReferenceKey(orgID, templateName, string(kv.Value))))
	}
	tr.ClearRange(ownedRange)
	return nil
}

// SetReferenceKeys replaces every reference key the node holds.
func (s *NodeStore) SetReferenceKeys(ctx context.Context, orgID, nodeID uuid.UUID, keys []node.ReferenceKey) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.set_reference_keys")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		return nil, writeReferenceKeys(tr, orgID, nodeID, keys)
	})
	return
}

// LookupReference returns the node holding encoded under templateName.
func (s *NodeStore) LookupReference(ctx context.Context, orgID uuid.UUID, templateName, encoded string) (id uuid.UUID, err error) {
	defer telemetry.FDBOp(ctx, "store.node.lookup_reference")(&err)
	value, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(nodeReferenceKey(orgID, templateName, encoded))).Get()
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("fdb lookup reference %q: %w", encoded, err)
	}
	raw, ok := value.([]byte)
	if !ok || len(raw) == 0 {
		return uuid.Nil, nil
	}
	held, parseErr := uuid.Parse(string(raw))
	if parseErr != nil {
		return uuid.Nil, fmt.Errorf("reference %q holds unparseable node id %q: %w", encoded, raw, parseErr)
	}
	return held, nil
}
```

- [ ] **Step 6: Add the counter methods**

In `internal/adapters/foundationdb/node.go`, add after `AllocateSequence`:

```go
// AllocateSequenceByKey atomically increments and returns the counter named by
// counterKey. The caller derives counterKey from a reference template minus its
// generated part, so the counter domain matches the uniqueness domain.
func (s *NodeStore) AllocateSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string) (seq int64, err error) {
	defer telemetry.FDBOp(ctx, "store.node.allocate_sequence_by_key")(&err)
	val, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		return bumpSequence(tr, fdb.Key(sequenceByKeyKey(orgID, counterKey)), 1)
	})
	if err != nil {
		return 0, fmt.Errorf("fdb allocate sequence for %q: %w", counterKey, err)
	}
	return val.(int64), nil
}

// SeedSequenceByKey raises the counter named by counterKey to at least value.
// It never lowers a counter, so re-running the repair backfill is safe.
func (s *NodeStore) SeedSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string, value int64) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.seed_sequence_by_key")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		key := fdb.Key(sequenceByKeyKey(orgID, counterKey))
		current, readErr := readSequence(tr, key)
		if readErr != nil {
			return nil, readErr
		}
		if current >= value {
			return nil, nil
		}
		writeSequence(tr, key, value)
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("fdb seed sequence for %q: %w", counterKey, err)
	}
	return nil
}

func readSequence(tr fdb.Transaction, key fdb.Key) (int64, error) {
	raw, err := tr.Get(key).Get()
	if err != nil {
		return 0, err
	}
	if len(raw) < 8 {
		return 0, nil
	}
	return int64(binary.LittleEndian.Uint64(raw)), nil
}

func writeSequence(tr fdb.Transaction, key fdb.Key, value int64) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(value))
	tr.Set(key, buf)
}

func bumpSequence(tr fdb.Transaction, key fdb.Key, delta int64) (int64, error) {
	current, err := readSequence(tr, key)
	if err != nil {
		return 0, err
	}
	next := current + delta
	writeSequence(tr, key, next)
	return next, nil
}
```

Add the key packer to `internal/adapters/foundationdb/keys.go`, after `sequenceKey`:

```go
// sequenceByKeyKey packs a counter keyed by a rendered reference-template
// prefix rather than by (scope, nodeType).
func sequenceByKeyKey(orgID uuid.UUID, counterKey string) []byte {
	return withPrefix(tuple.Tuple{keySequence, orgID.String(), counterKey}.Pack())
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `make test-integration`

Expected: PASS, including the four new reference-store tests.

- [ ] **Step 8: Build and commit**

Run: `make build`

```bash
git add internal/adapters/foundationdb/keys.go internal/adapters/foundationdb/node_reference.go internal/adapters/foundationdb/node.go internal/domain/node/repository.go internal/test/integration/reference_store_test.go
git commit -S -m "Add node reference key families and uniqueness enforcement helpers (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 3: Enforce reference keys on every write path

**Files:**
- Modify: `internal/adapters/foundationdb/node.go` (`CreateAtomic`, `UpdateAtomic`, `Delete`)
- Modify: `internal/domain/node/repository.go` (`CreateAtomic` and `UpdateAtomic` signatures)
- Test: `internal/test/integration/reference_store_test.go`

**Interfaces:**
- Consumes: `writeReferenceKeys`, `clearReferenceKeys` (Task 2); `node.ReferenceKey` (Task 2).
- Produces: `CreateAtomic(ctx, n, view, rels, indexedProps, referenceKeys []ReferenceKey, idempotency)` and `UpdateAtomic(ctx, n, view, oldProps, indexedProps, referenceKeys []ReferenceKey, relationshipChanges ...RelationshipChanges)`.

`Set` is intentionally unchanged. Its doc comment already states it does not reconcile indexes and is only correct when the caller has reconciled them externally. Reference keys follow that rule.

- [ ] **Step 1: Write the failing test**

Append to `internal/test/integration/reference_store_test.go`:

```go
// TestCreateAtomicRejectsADuplicateReference is the create-path enforcement
// assertion. A create carrying a reference another node holds must fail the
// whole transaction, leaving no partial node behind.
func TestCreateAtomicRejectsADuplicateReference(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	keys := []node.ReferenceKey{{TemplateName: "reference", Encoded: "FAN-42"}}

	holder := &node.Node{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, NodeType: "issue", Name: "Holder",
		Props: map[string]json.RawMessage{"scope_id": jsonStr(project.ID.String())},
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, holder, nil, nil, nil, keys, nil); err != nil {
		t.Fatalf("CreateAtomic for the holder: %v", err)
	}

	intruder := &node.Node{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, NodeType: "issue", Name: "Intruder",
		Props: map[string]json.RawMessage{"scope_id": jsonStr(project.ID.String())},
	}
	err := env.Stores.Nodes.CreateAtomic(env.Ctx, intruder, nil, nil, nil, keys, nil)
	if err == nil {
		t.Fatal("CreateAtomic with a taken reference: got nil error, want conflict")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateAtomic error = %v, want a domain.ErrConflict", err)
	}

	stored, getErr := env.Stores.Nodes.Get(env.Ctx, env.OrgID, intruder.ID)
	if getErr != nil {
		t.Fatalf("Get the rejected node: %v", getErr)
	}
	if stored != nil {
		t.Fatal("the rejected create left a node behind; the transaction did not roll back")
	}
}

// TestDeleteReleasesTheReference confirms deleting a node frees its reference
// for reuse, so the reverse index is cleared with the node.
func TestDeleteReleasesTheReference(t *testing.T) {
	env := SetupTestEnv(t)
	keys := []node.ReferenceKey{{TemplateName: "reference", Encoded: "FAN-55"}}
	doomed := &node.Node{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, NodeType: "issue", Name: "Doomed",
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, doomed, nil, nil, nil, keys, nil); err != nil {
		t.Fatalf("CreateAtomic: %v", err)
	}
	if err := env.Stores.Nodes.Delete(env.Ctx, env.OrgID, doomed.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	holder, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "FAN-55")
	if err != nil {
		t.Fatalf("LookupReference after delete: %v", err)
	}
	if holder != uuid.Nil {
		t.Fatalf("FAN-55 still held by %s after delete, want released", holder)
	}
}
```

Add `"encoding/json"` to the import block.

- [ ] **Step 2: Run the test to verify it fails**

Run: `make test-integration`

Expected: FAIL to compile, because `CreateAtomic` takes six arguments, not seven.

- [ ] **Step 3: Change the interface signatures**

In `internal/domain/node/repository.go`, replace the `UpdateAtomic` and `CreateAtomic` entries in `NodeRepository`:

```go
	// UpdateAtomic overwrites the node and view, reconciles the secondary
	// property indexes against oldProps for each name in indexedProps, and
	// replaces the node's reference keys with referenceKeys. Single FDB
	// transaction, so a duplicate reference rolls the whole update back and no
	// reader sees a half-rotated index.
	UpdateAtomic(
		ctx context.Context,
		n *Node,
		view *NodeView,
		oldProps map[string]json.RawMessage,
		indexedProps []string,
		referenceKeys []ReferenceKey,
		relationshipChanges ...RelationshipChanges,
	) error

	// CreateAtomic writes a new node plus its initial relationships and
	// reference keys in one FDB transaction. Used by NodeService.Create.
	// Sequence allocation is the caller's responsibility; the caller sets the
	// generated value in Props and renders referenceKeys before calling.
	//
	// indexedProps names the subset of Props that should receive a secondary
	// property index entry, and referenceKeys names the rendered uniqueness
	// keys. The caller resolves both from the NodeType and PropertyDef
	// registries; the storage layer reads neither.
	CreateAtomic(
		ctx context.Context,
		n *Node,
		view *NodeView,
		rels []*Relationship,
		indexedProps []string,
		referenceKeys []ReferenceKey,
		idempotency *IdempotencyRecord,
	) error
```

- [ ] **Step 4: Wire the storage implementations**

In `internal/adapters/foundationdb/node.go`, change `UpdateAtomic`'s signature and body:

```go
func (s *NodeStore) UpdateAtomic(
	ctx context.Context,
	n *node.Node,
	view *node.NodeView,
	oldProps map[string]json.RawMessage,
	indexedProps []string,
	referenceKeys []node.ReferenceKey,
	relationshipChanges ...node.RelationshipChanges,
) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.update_atomic")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		if err := writeNodeRecords(tr, n, view); err != nil {
			return nil, err
		}
		reconcilePropertyIndexes(tr, n, oldProps, indexedProps)
		if err := writeReferenceKeys(tr, n.OrgID, n.ID, referenceKeys); err != nil {
			return nil, err
		}
		if err := applyRelationshipChanges(ctx, tr, relationshipChanges); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return
}
```

Change `CreateAtomic`'s signature to accept `referenceKeys []node.ReferenceKey` immediately before `idempotency`, and add this block between the property-index loop (step 4 in that function) and the relationship loop (step 5):

```go
		// 4b. Reference uniqueness keys. A key held by another node fails the
		// whole transaction, so no partial node survives a duplicate.
		if err := writeReferenceKeys(tr, n.OrgID, n.ID, referenceKeys); err != nil {
			return nil, err
		}
```

In `Delete`, add inside the transaction, immediately after the property-index clear loop:

```go
		// Release every reference this node holds, read from the reverse index
		// so no NodeType metadata is needed here.
		if err := clearReferenceKeys(tr, orgID, nodeID); err != nil {
			return nil, err
		}
```

- [ ] **Step 5: Update every caller to compile**

Run: `make build`

Fix each reported call site by passing `nil` for `referenceKeys`. Task 4 replaces the `nil` at the service create path with real keys; every other call site (tests, seed, ops) keeps `nil` because those nodes carry no template yet.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `make test-integration`

Expected: PASS, including the two new write-path tests.

- [ ] **Step 7: Commit**

```bash
git add internal/adapters/foundationdb/node.go internal/domain/node/repository.go internal/test/integration/reference_store_test.go
git commit -S -m "Enforce reference keys in CreateAtomic, UpdateAtomic, and Delete (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 4: Render reference keys in the service and allocate from the derived counter

**Files:**
- Create: `internal/service/node_reference_keys.go`
- Modify: `internal/service/node_create_prepare.go`
- Modify: `internal/service/node_create.go`
- Test: `internal/service/node_reference_keys_test.go`

**Interfaces:**
- Consumes: `node.ReferenceTemplate`, `node.ReferenceRenderInput`, `NodeType.PrimaryReferenceTemplate` (Task 1); `node.ReferenceKey`, `AllocateSequenceByKey` (Task 2); the extended `CreateAtomic` (Task 3).
- Produces: `(*NodeService).referenceKeysFor(ctx context.Context, orgID uuid.UUID, nt *node.NodeType, nodeTypeKey string, scopeID uuid.UUID, props map[string]json.RawMessage) ([]node.ReferenceKey, error)`; `(*NodeService).scopeRefsFor(ctx context.Context, orgID, scopeID uuid.UUID) (map[string]string, error)`.

`scopeRefsFor` walks from the scope node upward and, for each ancestor declaring a feature, records that ancestor's own rendered reference. An ancestor whose reference strategy is a direct property renders from that property, which is how a project produces `FAN`.

- [ ] **Step 1: Write the failing test**

Create `internal/service/node_reference_keys_test.go`:

```go
package service

import (
	"encoding/json"
	"testing"

	"goodkind.io/tack/internal/domain/node"
)

// TestAllocateCreateSequenceUsesTheTemplateProperty proves the generated
// property name comes from the template, not from the literal "sequence". A
// type numbering into "number" must have "number" stamped.
func TestAllocateCreateSequenceUsesTheTemplateProperty(t *testing.T) {
	nodeType := &node.NodeType{
		TypeKey:  "ticket",
		Features: node.Features{node.FeatureHasSequenceID},
		ReferenceTemplates: []node.ReferenceTemplate{{
			Name:      "reference",
			IsPrimary: true,
			Generated: "number",
			Parts: []node.ReferencePart{
				{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
				{Kind: node.ReferencePartLiteral, Value: "-"},
				{Kind: node.ReferencePartProperty, Value: "number"},
			},
		}},
	}
	template := nodeType.PrimaryReferenceTemplate()
	if template == nil {
		t.Fatal("PrimaryReferenceTemplate = nil, want the declared template")
	}
	if template.Generated != "number" {
		t.Fatalf("generated property = %q, want %q", template.Generated, "number")
	}

	counterKey, err := template.CounterKey(node.ReferenceRenderInput{
		NodeTypeKey: "ticket",
		ScopeRefs:   map[string]string{node.FeatureIsScope: "OPS"},
	})
	if err != nil {
		t.Fatalf("CounterKey: %v", err)
	}
	rendered, err := template.Render(node.ReferenceRenderInput{
		NodeTypeKey: "ticket",
		Props:       map[string]json.RawMessage{"number": json.RawMessage("4")},
		ScopeRefs:   map[string]string{node.FeatureIsScope: "OPS"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if counterKey != "OPS-" {
		t.Fatalf("counter key = %q, want %q", counterKey, "OPS-")
	}
	if rendered != "OPS-4" {
		t.Fatalf("rendered reference = %q, want %q", rendered, "OPS-4")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/service/ -run TestAllocateCreateSequenceUsesTheTemplateProperty -v`

Expected: PASS on the domain assertions but FAIL to compile once Step 3 lands, because the test only pins the contract. Run it now and record that it passes; it guards the contract the next steps consume.

- [ ] **Step 3: Write the reference-key renderer**

Create `internal/service/node_reference_keys.go`:

```go
package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// maxScopeWalkDepth bounds the ancestor walk when collecting scope references.
const maxScopeWalkDepth = 32

// referenceKeysFor renders every reference key the node's type declares. An
// empty result means the type declares no template, which is legal.
func (s *NodeService) referenceKeysFor(
	ctx context.Context,
	orgID uuid.UUID,
	nt *node.NodeType,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) ([]node.ReferenceKey, error) {
	if len(nt.ReferenceTemplates) == 0 {
		return nil, nil
	}
	scopeRefs, err := s.scopeRefsFor(ctx, orgID, scopeID)
	if err != nil {
		return nil, err
	}
	input := node.ReferenceRenderInput{NodeTypeKey: nt.TypeKey, Props: props, ScopeRefs: scopeRefs}
	keys := make([]node.ReferenceKey, 0, len(nt.ReferenceTemplates))
	for _, template := range nt.ReferenceTemplates {
		encoded, renderErr := template.Render(input)
		if renderErr != nil {
			return nil, fmt.Errorf("node type %q: %w", nt.TypeKey, renderErr)
		}
		keys = append(keys, node.ReferenceKey{TemplateName: template.Name, Encoded: encoded})
	}
	return keys, nil
}

// scopeRefsFor walks from scopeID upward and records, for each feature an
// ancestor declares, that ancestor's own rendered reference. The nearest
// ancestor wins, so a deeper container shadows a shallower one.
func (s *NodeService) scopeRefsFor(ctx context.Context, orgID, scopeID uuid.UUID) (map[string]string, error) {
	refs := map[string]string{}
	if scopeID == uuid.Nil {
		return refs, nil
	}
	types, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list node types for scope references: %w", err)
	}
	typeIndex := node.BuildTypeIndex(types)
	currentID := scopeID
	for depth := 0; depth < maxScopeWalkDepth && currentID != uuid.Nil; depth++ {
		view, getErr := s.reader.Get(ctx, currentID)
		if getErr != nil {
			return nil, fmt.Errorf("get scope ancestor %s: %w", currentID, getErr)
		}
		if view == nil {
			break
		}
		ancestorType := typeIndex[view.NodeType]
		if ancestorType != nil {
			recordScopeRefs(refs, ancestorType, view)
		}
		currentID = uuidPropValue(view.Props, "parent_id")
	}
	return refs, nil
}

// recordScopeRefs stamps the ancestor's own reference under every feature it
// declares, unless a nearer ancestor already claimed that feature.
func recordScopeRefs(refs map[string]string, ancestorType *node.NodeType, view *node.NodeView) {
	reference := ancestorReference(ancestorType, view)
	if reference == "" {
		return
	}
	for _, feature := range ancestorType.Features {
		if _, taken := refs[feature]; taken {
			continue
		}
		refs[feature] = reference
	}
}

// ancestorReference renders an ancestor's own human reference. Only the
// direct-property strategy is supported as a scope reference, because a scope
// must be addressable without first resolving a further scope.
func ancestorReference(ancestorType *node.NodeType, view *node.NodeView) string {
	property := ancestorType.Reference.DirectAddressProperty()
	if property == "" {
		return ""
	}
	return stringPropertyValue(view.Props, property)
}

func uuidPropValue(props map[string]json.RawMessage, name string) uuid.UUID {
	value := stringPropertyValue(props, name)
	if value == "" {
		return uuid.Nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}
```

- [ ] **Step 4: Allocate into the template's property from the derived counter**

In `internal/service/node_create_prepare.go`, replace `allocateCreateSequence` entirely:

```go
// allocateCreateSequence fills the property the node type's primary reference
// template declares as generated, using a counter keyed by that template minus
// the generated part. The counter domain therefore always equals the uniqueness
// domain, so two nodes cannot be issued the same reference.
func (s *NodeService) allocateCreateSequence(
	ctx context.Context,
	log *slog.Logger,
	orgID uuid.UUID,
	nt *node.NodeType,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) error {
	template := nt.PrimaryReferenceTemplate()
	if template == nil || template.Generated == "" || scopeID == uuid.Nil {
		return nil
	}
	scopeRefs, err := s.scopeRefsFor(ctx, orgID, scopeID)
	if err != nil {
		return err
	}
	counterKey, err := template.CounterKey(node.ReferenceRenderInput{
		NodeTypeKey: nt.TypeKey,
		Props:       props,
		ScopeRefs:   scopeRefs,
	})
	if err != nil {
		return err
	}
	sequenceID, err := s.nodes.AllocateSequenceByKey(ctx, orgID, counterKey)
	if err != nil {
		log.ErrorContext(ctx, "node.create.allocate_sequence_failed",
			slog.String("node_type", nt.TypeKey),
			slog.String("counter_key", counterKey),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("allocate sequence for counter %q: %w", counterKey, err)
	}
	props[template.Generated] = json.RawMessage(strconv.FormatInt(sequenceID, 10))
	return nil
}
```

- [ ] **Step 5: Pass the rendered keys to the create**

In `internal/service/node_create.go`, after the `indexedProps` block (line 97 to 100), add:

```go
	referenceKeys, err := s.referenceKeysFor(ctx, orgID, nt, in.ScopeID, props)
	if err != nil {
		return nil, err
	}
```

Change the `createNodeAtomic` call to pass `referenceKeys` after `indexedProps`, and thread the parameter through `createNodeAtomic` in `internal/service/node_create_idempotency.go` to its `CreateAtomic` call.

- [ ] **Step 6: Run the tests and build**

Run: `go test ./internal/service/ -v` then `make build` then `make test-integration`

Expected: all pass. Existing tests still pass because no seeded type declares a template yet, so `referenceKeysFor` returns nil and `allocateCreateSequence` is a no-op. Task 8 turns it on.

- [ ] **Step 7: Commit**

```bash
git add internal/service/node_reference_keys.go internal/service/node_reference_keys_test.go internal/service/node_create_prepare.go internal/service/node_create.go internal/service/node_create_idempotency.go
git commit -S -m "Render reference keys and allocate from the template-derived counter (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 5: Point-read resolution with a fail-loud scan fallback

**Files:**
- Modify: `internal/adapters/mcp/tools/resolve_typed.go`
- Test: `internal/adapters/mcp/tools/resolve_typed_test.go`

**Interfaces:**
- Consumes: `LookupReference` (Task 2); `NodeType.PrimaryReferenceTemplate`, `ReferenceTemplate.Parse` (Task 1); `uniqueMatch` (existing, `resolve_reference_helpers.go` lines 99 to 109).
- Produces: `(*Resolver).resolveSequenceNodeID` resolving by point read first and, on a miss, by the prior scan routed through `uniqueMatch`.

- [ ] **Step 1: Write the failing test**

Append to `internal/adapters/mcp/tools/resolve_typed_test.go`:

```go
// TestResolveSequenceNodeIDFailsLoudOnDuplicates pins the fallback contract. A
// reference held by two nodes with no reference key written must raise an
// ambiguity error naming both, never silently return one of them.
func TestResolveSequenceNodeIDFailsLoudOnDuplicates(t *testing.T) {
	orgID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()

	workspace := &node.NodeView{ID: workspaceID, OrgID: orgID, NodeType: "workspace", Props: map[string]json.RawMessage{"slug": mustRaw(t, "main")}}
	project := &node.NodeView{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"identifier": mustRaw(t, "FAN"), "parent_id": mustRaw(t, workspaceID.String())}}
	reader := &resolverReader{views: map[uuid.UUID]*node.NodeView{workspaceID: workspace, projectID: project}, workspaces: []*node.NodeView{workspace}}
	repo := &fakeNodeRepo{scopeChildren: map[string][]*node.Node{
		"project:identifier:\"FAN\"": {{ID: projectID, OrgID: orgID, NodeType: "project", Props: map[string]json.RawMessage{"parent_id": mustRaw(t, workspaceID.String())}}},
		"issue:sequence:13": {
			{ID: firstID, OrgID: orgID, NodeType: "issue", Props: map[string]json.RawMessage{"scope_id": mustRaw(t, projectID.String())}},
			{ID: secondID, OrgID: orgID, NodeType: "issue", Props: map[string]json.RawMessage{"scope_id": mustRaw(t, projectID.String())}},
		},
	}}
	projectType := &node.NodeType{TypeKey: "project", Slug: "project", CanLiveUnder: []string{"workspace"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"}}
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue", CanLiveUnder: []string{"project"}, Reference: node.ReferenceConfig{Strategy: node.ReferenceScopedSequence, Property: "sequence"}}
	resolver := &Resolver{
		nodes: repo, reader: reader, members: &fakeMembers{orgIDs: []uuid.UUID{orgID}},
		entryPointTypeKey: "workspace", entryPointSlug: "workspace",
		scopeChain: []ScopeLevel{{TypeKey: "project", Slug: "project", ParamName: "project_reference"}},
		typeIndex:  map[string]*node.NodeType{"project": projectType, "issue": issueType},
	}
	ctx := audit.WithScopeBuilder(auth.WithUser(context.Background(), uuid.New()))

	_, err := resolver.ResolveTypedNodeID(ctx, issueType, "FAN-13")
	if err == nil {
		t.Fatal("ResolveTypedNodeID on a duplicated reference: got nil error, want ambiguity")
	}
	if !strings.Contains(err.Error(), "matched multiple nodes") {
		t.Fatalf("error = %v, want it to report multiple matches", err)
	}
	if !strings.Contains(err.Error(), firstID.String()) || !strings.Contains(err.Error(), secondID.String()) {
		t.Fatalf("error = %v, want both candidate UUIDs named so an operator can recover", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/adapters/mcp/tools/ -run TestResolveSequenceNodeIDFailsLoudOnDuplicates -v`

Expected: FAIL, because the current code returns the first candidate with a nil error.

- [ ] **Step 3: Rewrite the resolution path**

Replace `resolveSequenceNodeID` in `internal/adapters/mcp/tools/resolve_typed.go`:

```go
func (r *Resolver) resolveSequenceNodeID(ctx context.Context, input string, typeKeys []string) (uuid.UUID, error) {
	projectReference, seqID, err := ParseNodeIdentifier(input)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid node_id %q: must be a UUID or sequence reference like TACK-65: %w", input, domain.ErrInvalidArgument)
	}
	userID, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, fmt.Errorf("unauthenticated")
	}
	workspaces, err := r.WorkspacesForUser(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, workspace := range workspaces {
		if len(r.scopeChain) == 0 {
			continue
		}
		if id, found := r.referenceKeyHolder(ctx, workspace.OrgID, typeKeys, input); found {
			return id, nil
		}
		scopeNode, scopeErr := r.ResolveScope(ctx, workspace, r.scopeChain[0], projectReference)
		if scopeErr != nil {
			continue
		}
		id, matchErr := r.scanSequenceCandidates(ctx, workspace.OrgID, typeKeys, seqID, scopeNode.ID, input)
		if matchErr != nil {
			if errors.Is(matchErr, domain.ErrInvalidArgument) {
				return uuid.Nil, matchErr
			}
			continue
		}
		return id, nil
	}
	return uuid.Nil, fmt.Errorf("reference %q: %w", input, domain.ErrNotFound)
}

// referenceKeyHolder resolves the reference through the uniqueness index. A hit
// is authoritative and needs no scope resolution or candidate scan.
func (r *Resolver) referenceKeyHolder(ctx context.Context, orgID uuid.UUID, typeKeys []string, input string) (uuid.UUID, bool) {
	for _, typeKey := range typeKeys {
		nodeType := r.typeIndex[typeKey]
		if nodeType == nil {
			continue
		}
		template := nodeType.PrimaryReferenceTemplate()
		if template == nil {
			continue
		}
		held, err := r.nodes.LookupReference(ctx, orgID, template.Name, input)
		if err != nil || held == uuid.Nil {
			continue
		}
		stampAuditOrg(ctx, orgID)
		return held, true
	}
	return uuid.Nil, false
}

// scanSequenceCandidates is the fallback for nodes written before the reference
// index existed. It collects every candidate and routes them through
// uniqueMatch, so a duplicate raises an ambiguity error naming both nodes rather
// than silently resolving to one of them.
func (r *Resolver) scanSequenceCandidates(
	ctx context.Context,
	orgID uuid.UUID,
	typeKeys []string,
	seqID int,
	scopeID uuid.UUID,
	input string,
) (uuid.UUID, error) {
	rawSeq, _ := json.Marshal(int64(seqID))
	matches := map[uuid.UUID]struct{}{}
	for _, typeKey := range typeKeys {
		nodeType := r.typeIndex[typeKey]
		propName := "sequence"
		if nodeType != nil && nodeType.Reference.Property != "" {
			propName = nodeType.Reference.Property
		}
		candidates, err := r.nodes.ListByProperty(ctx, orgID, typeKey, propName, rawSeq)
		if err != nil {
			continue
		}
		for _, candidate := range candidates {
			if r.nodeBelongsToScope(ctx, candidate, scopeID) {
				matches[candidate.ID] = struct{}{}
			}
		}
	}
	id, err := uniqueMatch(matches, "reference", input)
	if err != nil {
		return uuid.Nil, err
	}
	stampAuditOrg(ctx, orgID)
	return id, nil
}
```

Add `"errors"` to the import block.

- [ ] **Step 4: Name both candidates in the ambiguity error**

In `internal/adapters/mcp/tools/resolve_reference_helpers.go`, replace `uniqueMatch`:

```go
// uniqueMatch returns the single match, or an error. An ambiguous result names
// every candidate UUID, because a caller holding an ambiguous reference has no
// other way to reach the nodes behind it.
func uniqueMatch(matches map[uuid.UUID]struct{}, kind, input string) (uuid.UUID, error) {
	switch len(matches) {
	case 0:
		return uuid.Nil, fmt.Errorf("%s %q: %w", kind, input, domain.ErrNotFound)
	case 1:
		for id := range matches {
			return id, nil
		}
	}
	candidates := make([]string, 0, len(matches))
	for id := range matches {
		candidates = append(candidates, id.String())
	}
	sort.Strings(candidates)
	return uuid.Nil, fmt.Errorf(
		"%s %q matched multiple nodes (%s): %w",
		kind, input, strings.Join(candidates, ", "), domain.ErrInvalidArgument,
	)
}
```

Add `"sort"` to the import block.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/adapters/mcp/tools/ -v`

Expected: PASS, including the new duplicate test.

- [ ] **Step 6: Build and commit**

Run: `make build`

```bash
git add internal/adapters/mcp/tools/resolve_typed.go internal/adapters/mcp/tools/resolve_reference_helpers.go internal/adapters/mcp/tools/resolve_typed_test.go
git commit -S -m "Resolve references by point read and fail loud on legacy duplicates (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 6: Duplicate-reference detect operation

**Files:**
- Create: `internal/ops/reference_duplicates.go`
- Test: `internal/test/integration/reference_duplicates_test.go`

**Interfaces:**
- Consumes: `node.ReferenceTemplate.Render`, `NodeType.ReferenceTemplates` (Task 1); `ops.Register`, `ops.Env` (existing, `internal/ops/ops.go` lines 39 to 54).
- Produces: operation named `reference.duplicates`; exported `ops.FindDuplicateReferences(ctx context.Context, env *Env) ([]DuplicateReference, error)`; type `ops.DuplicateReference{OrgID uuid.UUID, TemplateName string, Encoded string, NodeIDs []uuid.UUID}`.

Task 7 reuses `FindDuplicateReferences`, so it is exported rather than folded into the operation body.

- [ ] **Step 1: Write the failing test**

Create `internal/test/integration/reference_duplicates_test.go`:

```go
package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/ops"
)

// TestFindDuplicateReferencesReportsBothHolders is the detect contract: two
// nodes rendering one reference appear as a single group naming both.
func TestFindDuplicateReferencesReportsBothHolders(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)

	epic := mustCreate(t, env, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "epic", Name: "Epic one", ActorID: actor,
	})
	issue := mustCreate(t, env, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Issue one", ActorID: actor,
	})

	duplicates, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences: %v", err)
	}
	if len(duplicates) != 1 {
		t.Fatalf("duplicate groups = %d, want 1; groups = %+v", len(duplicates), duplicates)
	}
	group := duplicates[0]
	if len(group.NodeIDs) != 2 {
		t.Fatalf("group %q holds %d nodes, want 2", group.Encoded, len(group.NodeIDs))
	}
	held := map[uuid.UUID]bool{group.NodeIDs[0]: true, group.NodeIDs[1]: true}
	if !held[epic.ID] || !held[issue.ID] {
		t.Fatalf("group %q holds %v, want the epic %s and the issue %s", group.Encoded, group.NodeIDs, epic.ID, issue.ID)
	}
}
```

This test requires `SetupTestEnv` to expose an `*ops.Env`. Add an `Ops *ops.Env` field to the integration `TestEnv` and populate it from the same stores and pool the env already opens.

- [ ] **Step 2: Run the test to verify it fails**

Run: `make test-integration`

Expected: FAIL to compile, with `undefined: ops.FindDuplicateReferences`.

- [ ] **Step 3: Write the operation**

Create `internal/ops/reference_duplicates.go`:

```go
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "reference.duplicates",
		Description: "Report every human-readable node reference held by more than one node, across every org and node type. Read-only.",
		Run:         runReferenceDuplicates,
	})
}

// DuplicateReference names one rendered reference held by more than one node.
type DuplicateReference struct {
	OrgID        uuid.UUID
	TemplateName string
	// Encoded is the rendered reference, for example "FAN-13".
	Encoded string
	// NodeIDs are the holders, sorted so the output is stable across runs.
	NodeIDs []uuid.UUID
}

func runReferenceDuplicates(ctx context.Context, env *Env) error {
	duplicates, err := FindDuplicateReferences(ctx, env)
	if err != nil {
		return err
	}
	for _, duplicate := range duplicates {
		env.Log.WarnContext(ctx, "reference.duplicate",
			slog.String("org_id", duplicate.OrgID.String()),
			slog.String("template", duplicate.TemplateName),
			slog.String("reference", duplicate.Encoded),
			slog.Any("node_ids", duplicate.NodeIDs),
		)
	}
	env.Log.InfoContext(ctx, "reference.duplicates.completed", slog.Int("groups", len(duplicates)))
	return nil
}

// FindDuplicateReferences renders every node's reference templates and returns
// each rendered value held by more than one node. Grouping is by rendered
// value, so it catches duplicates across node types as well as within one.
func FindDuplicateReferences(ctx context.Context, env *Env) ([]DuplicateReference, error) {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return nil, err
	}
	var duplicates []DuplicateReference
	for orgID := range orgIDs {
		orgDuplicates, orgErr := findOrgDuplicateReferences(ctx, env, orgID)
		if orgErr != nil {
			return nil, orgErr
		}
		duplicates = append(duplicates, orgDuplicates...)
	}
	sort.Slice(duplicates, func(i, j int) bool {
		if duplicates[i].OrgID != duplicates[j].OrgID {
			return duplicates[i].OrgID.String() < duplicates[j].OrgID.String()
		}
		return duplicates[i].Encoded < duplicates[j].Encoded
	})
	return duplicates, nil
}

func findOrgDuplicateReferences(ctx context.Context, env *Env, orgID uuid.UUID) ([]DuplicateReference, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list node types for org %s: %w", orgID, err)
	}
	holders := map[string][]uuid.UUID{}
	templateOf := map[string]string{}
	encodedOf := map[string]string{}
	for _, nodeType := range nodeTypes {
		if len(nodeType.ReferenceTemplates) == 0 {
			continue
		}
		views, listErr := env.Stores.Views.List(ctx, node.NodeListQuery{OrgID: orgID, NodeType: nodeType.TypeKey})
		if listErr != nil {
			return nil, fmt.Errorf("list %s nodes in org %s: %w", nodeType.TypeKey, orgID, listErr)
		}
		for _, view := range views {
			collectRenderedReferences(ctx, env, orgID, nodeType, view, holders, templateOf, encodedOf)
		}
	}
	return duplicateGroups(orgID, holders, templateOf, encodedOf), nil
}

func collectRenderedReferences(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	nodeType *node.NodeType,
	view *node.NodeView,
	holders map[string][]uuid.UUID,
	templateOf map[string]string,
	encodedOf map[string]string,
) {
	scopeRefs, err := scopeReferencesForView(ctx, env, orgID, view)
	if err != nil {
		return
	}
	input := node.ReferenceRenderInput{NodeTypeKey: nodeType.TypeKey, Props: view.Props, ScopeRefs: scopeRefs}
	for _, template := range nodeType.ReferenceTemplates {
		encoded, renderErr := template.Render(input)
		if renderErr != nil {
			continue
		}
		group := template.Name + "\x00" + encoded
		holders[group] = append(holders[group], view.ID)
		templateOf[group] = template.Name
		encodedOf[group] = encoded
	}
}

func duplicateGroups(
	orgID uuid.UUID,
	holders map[string][]uuid.UUID,
	templateOf map[string]string,
	encodedOf map[string]string,
) []DuplicateReference {
	var duplicates []DuplicateReference
	for group, ids := range holders {
		if len(ids) < 2 {
			continue
		}
		sorted := append([]uuid.UUID(nil), ids...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() < sorted[j].String() })
		duplicates = append(duplicates, DuplicateReference{
			OrgID:        orgID,
			TemplateName: templateOf[group],
			Encoded:      encodedOf[group],
			NodeIDs:      sorted,
		})
	}
	return duplicates
}

// scopeReferencesForView mirrors the service-side scope walk so a rendered
// reference here matches the one the service would write.
func scopeReferencesForView(ctx context.Context, env *Env, orgID uuid.UUID, view *node.NodeView) (map[string]string, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	refs := map[string]string{}
	currentID := rawUUIDProp(view.Props, "scope_id")
	if currentID == uuid.Nil {
		currentID = rawUUIDProp(view.Props, "parent_id")
	}
	for depth := 0; depth < maxRepairParentDepth && currentID != uuid.Nil; depth++ {
		ancestor, getErr := env.Stores.Views.Get(ctx, currentID)
		if getErr != nil || ancestor == nil {
			break
		}
		ancestorType := typeIndex[ancestor.NodeType]
		if ancestorType != nil {
			property := ancestorType.Reference.DirectAddressProperty()
			if property != "" {
				if value := stringProp(ancestor.Props, property); value != "" {
					for _, feature := range ancestorType.Features {
						if _, taken := refs[feature]; !taken {
							refs[feature] = value
						}
					}
				}
			}
		}
		currentID = rawUUIDProp(ancestor.Props, "parent_id")
	}
	return refs, nil
}

var _ = json.Marshal
```

Remove the trailing `var _ = json.Marshal` line and the `encoding/json` import if the compiler reports them unused.

- [ ] **Step 4: Run the test to verify it passes**

Run: `make test-integration`

Expected: FAIL, because no seeded node type declares a template yet. Temporarily add the issue and epic templates in the test's setup to confirm the detection logic, then remove them. Task 8 makes this test pass without local setup.

Record the outcome and proceed. Task 8 reruns it.

- [ ] **Step 5: Build and commit**

Run: `make build`

```bash
git add internal/ops/reference_duplicates.go internal/test/integration/reference_duplicates_test.go
git commit -S -m "Add reference.duplicates detect operation (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 7: Renumber and backfill operation

**Files:**
- Create: `internal/ops/repair_reference_uniqueness.go`
- Test: `internal/test/integration/reference_repair_test.go`

**Interfaces:**
- Consumes: `FindDuplicateReferences`, `DuplicateReference`, `scopeReferencesForView` (Task 6); `SetReferenceKeys`, `AllocateSequenceByKey`, `SeedSequenceByKey` (Task 2); `node.ReferenceTemplate` (Task 1).
- Produces: operation named `repair.reference_uniqueness`; exported `ops.RepairReferenceUniqueness(ctx context.Context, env *Env, opts RepairReferenceOptions) (RepairReferenceReport, error)`; types `ops.RepairReferenceOptions{Execute bool, Keep string}` and `ops.RepairReferenceReport{Renumbered []ReferenceRename, CountersSeeded int, KeysWritten int}`, `ops.ReferenceRename{NodeID uuid.UUID, From string, To string}`.

`Keep` accepts `oldest` and `newest`. `oldest` orders by UUIDv7, which sorts by creation time. The default is `oldest`. Which node is retained is this operation's policy; the storage layer holds no opinion.

Order of work inside one run, and the order matters:

1. Seed every counter to the highest generated value observed in its scope. A newly derived counter key starts at zero, so skipping this reissues values already in use.
2. Renumber duplicates, oldest or newest retained per `Keep`.
3. Write reference keys for every node carrying a template, so enforcement data exists for nodes created before templates did.

- [ ] **Step 1: Write the failing test**

Create `internal/test/integration/reference_repair_test.go`:

```go
package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/ops"
)

// TestRepairReferenceUniquenessRenumbersAndHoldsTheLine drives the full repair:
// a duplicate is renumbered, the retained node keeps its reference, and a
// subsequent create cannot collide because the counter was seeded past the
// high-water mark.
func TestRepairReferenceUniquenessRenumbersAndHoldsTheLine(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)

	epic := mustCreate(t, env, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "epic", Name: "Epic one", ActorID: actor,
	})
	issue := mustCreate(t, env, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Issue one", ActorID: actor,
	})

	report, err := ops.RepairReferenceUniqueness(env.Ctx, env.Ops, ops.RepairReferenceOptions{Execute: true, Keep: "oldest"})
	if err != nil {
		t.Fatalf("RepairReferenceUniqueness: %v", err)
	}
	if len(report.Renumbered) != 1 {
		t.Fatalf("renumbered %d nodes, want 1; report = %+v", len(report.Renumbered), report)
	}
	if report.Renumbered[0].NodeID != issue.ID {
		t.Fatalf("renumbered node = %s, want the newer issue %s (the epic is older and keeps its reference)", report.Renumbered[0].NodeID, issue.ID)
	}
	_ = epic

	remaining, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences after repair: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("duplicates after repair = %+v, want none", remaining)
	}

	// A fresh create must not reuse a number already in play.
	fresh := mustCreate(t, env, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Fresh", ActorID: actor,
	})
	after, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences after a fresh create: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("a create after repair produced duplicates %+v; the counter was not seeded past the high-water mark", after)
	}
	_ = fresh
}

// TestRepairReferenceUniquenessDryRunChangesNothing confirms the default mode
// reports without writing.
func TestRepairReferenceUniquenessDryRunChangesNothing(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	mustCreate(t, env, service.CreateInput{ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "epic", Name: "Epic", ActorID: actor})
	mustCreate(t, env, service.CreateInput{ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Issue", ActorID: actor})

	report, err := ops.RepairReferenceUniqueness(env.Ctx, env.Ops, ops.RepairReferenceOptions{Execute: false, Keep: "oldest"})
	if err != nil {
		t.Fatalf("RepairReferenceUniqueness dry run: %v", err)
	}
	if len(report.Renumbered) != 1 {
		t.Fatalf("dry run reported %d renames, want 1", len(report.Renumbered))
	}

	remaining, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences after the dry run: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("duplicates after a dry run = %d, want the original 1 left untouched", len(remaining))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `make test-integration`

Expected: FAIL to compile, with `undefined: ops.RepairReferenceUniqueness`.

- [ ] **Step 3: Write the repair operation**

Create `internal/ops/repair_reference_uniqueness.go`:

```go
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// keepOldest and keepNewest name the retention policies this operation offers.
// Which node keeps a contested reference is a policy of this backfill, not a
// rule in the storage layer.
const (
	keepOldest = "oldest"
	keepNewest = "newest"
)

func init() {
	Register(Operation{
		Name:        "repair.reference_uniqueness",
		Description: "Seed reference counters to their high-water mark, renumber duplicate references, and write the uniqueness keys for every node. Reports without writing unless executed.",
		Run:         runRepairReferenceUniqueness,
	})
}

// RepairReferenceOptions configures one repair run.
type RepairReferenceOptions struct {
	// Execute applies the changes. False reports and writes nothing.
	Execute bool
	// Keep selects which node retains a contested reference: "oldest" or
	// "newest". Empty means "oldest".
	Keep string
}

// ReferenceRename records one reassignment.
type ReferenceRename struct {
	NodeID uuid.UUID
	From   string
	To     string
}

// RepairReferenceReport summarises one repair run.
type RepairReferenceReport struct {
	Renumbered     []ReferenceRename
	CountersSeeded int
	KeysWritten    int
}

func runRepairReferenceUniqueness(ctx context.Context, env *Env) error {
	report, err := RepairReferenceUniqueness(ctx, env, RepairReferenceOptions{Execute: false, Keep: keepOldest})
	if err != nil {
		return err
	}
	for _, rename := range report.Renumbered {
		env.Log.InfoContext(ctx, "reference.rename.planned",
			slog.String("node_id", rename.NodeID.String()),
			slog.String("from", rename.From),
			slog.String("to", rename.To),
		)
	}
	env.Log.InfoContext(ctx, "repair.reference_uniqueness.completed",
		slog.Int("renames", len(report.Renumbered)),
		slog.Int("counters_seeded", report.CountersSeeded),
		slog.Int("keys_written", report.KeysWritten),
	)
	return nil
}

// RepairReferenceUniqueness seeds counters, renumbers duplicates, and writes
// reference keys. Counter seeding runs first: a newly derived counter key starts
// at zero, so renumbering before seeding would reissue values already in use.
func RepairReferenceUniqueness(ctx context.Context, env *Env, opts RepairReferenceOptions) (RepairReferenceReport, error) {
	if opts.Keep == "" {
		opts.Keep = keepOldest
	}
	if opts.Keep != keepOldest && opts.Keep != keepNewest {
		return RepairReferenceReport{}, fmt.Errorf("keep policy %q: want %q or %q", opts.Keep, keepOldest, keepNewest)
	}
	report := RepairReferenceReport{}
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return report, err
	}
	for orgID := range orgIDs {
		seeded, seedErr := seedReferenceCounters(ctx, env, orgID, opts.Execute)
		if seedErr != nil {
			return report, seedErr
		}
		report.CountersSeeded += seeded
	}

	duplicates, err := FindDuplicateReferences(ctx, env)
	if err != nil {
		return report, err
	}
	for _, duplicate := range duplicates {
		renames, renameErr := renumberDuplicateGroup(ctx, env, duplicate, opts)
		if renameErr != nil {
			return report, renameErr
		}
		report.Renumbered = append(report.Renumbered, renames...)
	}

	if opts.Execute {
		for orgID := range orgIDs {
			written, writeErr := writeAllReferenceKeys(ctx, env, orgID)
			if writeErr != nil {
				return report, writeErr
			}
			report.KeysWritten += written
		}
	}
	return report, nil
}

// seedReferenceCounters raises every counter to the highest generated value its
// scope already holds, so no counter reissues a value in use.
func seedReferenceCounters(ctx context.Context, env *Env, orgID uuid.UUID, execute bool) (int, error) {
	highest := map[string]int64{}
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		return 0, fmt.Errorf("list node types for org %s: %w", orgID, err)
	}
	for _, nodeType := range nodeTypes {
		template := nodeType.PrimaryReferenceTemplate()
		if template == nil || template.Generated == "" {
			continue
		}
		views, listErr := env.Stores.Views.List(ctx, node.NodeListQuery{OrgID: orgID, NodeType: nodeType.TypeKey})
		if listErr != nil {
			return 0, fmt.Errorf("list %s nodes in org %s: %w", nodeType.TypeKey, orgID, listErr)
		}
		for _, view := range views {
			recordHighestGenerated(ctx, env, orgID, nodeType, *template, view, highest)
		}
	}
	if !execute {
		return len(highest), nil
	}
	for counterKey, value := range highest {
		if err := env.Stores.Nodes.SeedSequenceByKey(ctx, orgID, counterKey, value); err != nil {
			return 0, fmt.Errorf("seed counter %q in org %s: %w", counterKey, orgID, err)
		}
	}
	return len(highest), nil
}

func recordHighestGenerated(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	nodeType *node.NodeType,
	template node.ReferenceTemplate,
	view *node.NodeView,
	highest map[string]int64,
) {
	scopeRefs, err := scopeReferencesForView(ctx, env, orgID, view)
	if err != nil {
		return
	}
	counterKey, err := template.CounterKey(node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       view.Props,
		ScopeRefs:   scopeRefs,
	})
	if err != nil {
		return
	}
	value := numberPropValue(view.Props, template.Generated)
	if value > highest[counterKey] {
		highest[counterKey] = value
	}
}

// renumberDuplicateGroup reassigns every node in the group except the retained
// one. Ordering is by UUIDv7, which sorts by creation time.
func renumberDuplicateGroup(ctx context.Context, env *Env, duplicate DuplicateReference, opts RepairReferenceOptions) ([]ReferenceRename, error) {
	ordered := append([]uuid.UUID(nil), duplicate.NodeIDs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].String() < ordered[j].String() })
	if opts.Keep == keepNewest {
		ordered = append(ordered[1:], ordered[0])
	}
	var renames []ReferenceRename
	for _, nodeID := range ordered[1:] {
		rename, err := renumberOneNode(ctx, env, duplicate, nodeID, opts.Execute)
		if err != nil {
			return nil, err
		}
		renames = append(renames, rename)
	}
	return renames, nil
}

func renumberOneNode(ctx context.Context, env *Env, duplicate DuplicateReference, nodeID uuid.UUID, execute bool) (ReferenceRename, error) {
	view, err := env.Stores.Views.Get(ctx, nodeID)
	if err != nil || view == nil {
		return ReferenceRename{}, fmt.Errorf("get node %s for renumber: %w", nodeID, err)
	}
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, duplicate.OrgID)
	if err != nil {
		return ReferenceRename{}, err
	}
	nodeType := node.BuildTypeIndex(nodeTypes)[view.NodeType]
	if nodeType == nil {
		return ReferenceRename{}, fmt.Errorf("node %s has unknown type %q", nodeID, view.NodeType)
	}
	template := nodeType.PrimaryReferenceTemplate()
	if template == nil || template.Generated == "" {
		return ReferenceRename{}, fmt.Errorf("node type %q declares no generated reference part; node %s cannot be renumbered", nodeType.TypeKey, nodeID)
	}
	scopeRefs, err := scopeReferencesForView(ctx, env, duplicate.OrgID, view)
	if err != nil {
		return ReferenceRename{}, err
	}
	counterKey, err := template.CounterKey(node.ReferenceRenderInput{NodeTypeKey: nodeType.TypeKey, Props: view.Props, ScopeRefs: scopeRefs})
	if err != nil {
		return ReferenceRename{}, err
	}
	if !execute {
		return ReferenceRename{NodeID: nodeID, From: duplicate.Encoded, To: "(next value on " + counterKey + ")"}, nil
	}
	next, err := env.Stores.Nodes.AllocateSequenceByKey(ctx, duplicate.OrgID, counterKey)
	if err != nil {
		return ReferenceRename{}, fmt.Errorf("allocate a replacement value for node %s: %w", nodeID, err)
	}
	updated, err := env.Stores.Nodes.Get(ctx, duplicate.OrgID, nodeID)
	if err != nil || updated == nil {
		return ReferenceRename{}, fmt.Errorf("read node %s for renumber: %w", nodeID, err)
	}
	oldProps := cloneProps(updated.Props)
	updated.Props[template.Generated] = json.RawMessage(strconv.FormatInt(next, 10))
	renderedInput := node.ReferenceRenderInput{NodeTypeKey: nodeType.TypeKey, Props: updated.Props, ScopeRefs: scopeRefs}
	rendered, err := template.Render(renderedInput)
	if err != nil {
		return ReferenceRename{}, err
	}
	updatedView := viewFromRepairedNode(updated)
	propertyDefs, err := env.Stores.PropertyDefs.List(ctx, duplicate.OrgID)
	if err != nil {
		return ReferenceRename{}, err
	}
	keys := []node.ReferenceKey{{TemplateName: template.Name, Encoded: rendered}}
	if err := env.Stores.Nodes.UpdateAtomic(ctx, updated, updatedView, oldProps, indexedPropsFor(nodeType, propertyDefs), keys); err != nil {
		return ReferenceRename{}, fmt.Errorf("write renumbered node %s: %w", nodeID, err)
	}
	return ReferenceRename{NodeID: nodeID, From: duplicate.Encoded, To: rendered}, nil
}

// writeAllReferenceKeys backfills the uniqueness keys for every node carrying a
// template, so enforcement data exists for nodes created before templates did.
func writeAllReferenceKeys(ctx context.Context, env *Env, orgID uuid.UUID) (int, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, nodeType := range nodeTypes {
		if len(nodeType.ReferenceTemplates) == 0 {
			continue
		}
		views, listErr := env.Stores.Views.List(ctx, node.NodeListQuery{OrgID: orgID, NodeType: nodeType.TypeKey})
		if listErr != nil {
			return 0, listErr
		}
		for _, view := range views {
			scopeRefs, refErr := scopeReferencesForView(ctx, env, orgID, view)
			if refErr != nil {
				continue
			}
			input := node.ReferenceRenderInput{NodeTypeKey: nodeType.TypeKey, Props: view.Props, ScopeRefs: scopeRefs}
			keys := make([]node.ReferenceKey, 0, len(nodeType.ReferenceTemplates))
			for _, template := range nodeType.ReferenceTemplates {
				encoded, renderErr := template.Render(input)
				if renderErr != nil {
					continue
				}
				keys = append(keys, node.ReferenceKey{TemplateName: template.Name, Encoded: encoded})
			}
			if len(keys) == 0 {
				continue
			}
			if err := env.Stores.Nodes.SetReferenceKeys(ctx, orgID, view.ID, keys); err != nil {
				return 0, fmt.Errorf("write reference keys for node %s: %w", view.ID, err)
			}
			written += len(keys)
		}
	}
	return written, nil
}

func numberPropValue(props map[string]json.RawMessage, name string) int64 {
	raw, ok := props[name]
	if !ok || len(raw) == 0 {
		return 0
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0
	}
	return int64(value)
}

// viewFromRepairedNode builds the materialized view for a node the repair
// rewrote, mirroring the fields the service stamps.
func viewFromRepairedNode(n *node.Node) *node.NodeView {
	return &node.NodeView{
		ID:        n.ID,
		OrgID:     n.OrgID,
		NodeType:  n.NodeType,
		Name:      n.Name,
		Props:     cloneProps(n.Props),
		CreatedBy: n.CreatedBy,
		UpdatedBy: n.UpdatedBy,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test-integration`

Expected: FAIL until Task 8 seeds templates. Confirm the compile is clean and the logic runs, then proceed. Task 8 reruns both tests.

- [ ] **Step 5: Build and commit**

Run: `make build`

```bash
git add internal/ops/repair_reference_uniqueness.go internal/test/integration/reference_repair_test.go
git commit -S -m "Add repair.reference_uniqueness renumber and backfill operation (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 8: Seed reference templates on the built-in types

**Files:**
- Modify: `internal/service/seed.go`
- Test: `internal/test/integration/reference_duplicates_test.go` and `internal/test/integration/reference_repair_test.go` (both from earlier tasks, now expected to pass unmodified)

**Interfaces:**
- Consumes: `node.ReferenceTemplate` (Task 1).
- Produces: `issue`, `epic`, `cycle`, and `module` node types each carrying a primary reference template of scope reference, hyphen, generated `sequence`, with no node-type part.

Omitting the node-type part is the decision that makes epic `FAN-1` and issue `FAN-1` a conflict. An operator wanting independent numbering per type adds a `node_type` part to their own template.

- [ ] **Step 1: Run the earlier tests to confirm they still fail**

Run: `make test-integration`

Expected: the Task 6 and Task 7 tests FAIL, because no seeded type declares a template.

- [ ] **Step 2: Add the shared template constructor**

In `internal/service/seed.go`, add above the node type definitions:

```go
// scopedSequenceTemplate is the reference contract for work-item types: the
// enclosing scope's reference, a hyphen, then a generated sequence. The node
// type is deliberately absent, so every work-item type in one scope shares a
// single numbering space and "FAN-13" names exactly one node.
func scopedSequenceTemplate() []node.ReferenceTemplate {
	return []node.ReferenceTemplate{{
		Name:      "reference",
		IsPrimary: true,
		Generated: "sequence",
		Parts: []node.ReferencePart{
			{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
			{Kind: node.ReferencePartLiteral, Value: "-"},
			{Kind: node.ReferencePartProperty, Value: "sequence"},
		},
	}}
}
```

- [ ] **Step 3: Attach it to every sequence-bearing built-in type**

For each of the `issue`, `epic`, `cycle`, and `module` NodeType literals in `internal/service/seed.go`, add:

```go
			ReferenceTemplates: scopedSequenceTemplate(),
```

Locate them by their existing `Features` entry containing `node.FeatureHasSequenceID`, not by their slug.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `make test-integration`

Expected: PASS, including both Task 6 tests and both Task 7 tests, unmodified.

- [ ] **Step 5: Build and commit**

Run: `make build`

```bash
git add internal/service/seed.go
git commit -S -m "Seed a shared scoped-sequence reference template on work-item types (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 9: End-to-end regression for both duplicate mechanisms

**Files:**
- Create: `internal/test/integration/reference_uniqueness_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1 through 8.
- Produces: no new API. This task proves the defect is closed.

Both reproductions drive real code paths. Neither writes synthetic data, so a passing test proves the behavior rather than restating it.

- [ ] **Step 1: Write the regression test**

Create `internal/test/integration/reference_uniqueness_test.go`:

```go
package integration

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
)

// TestCrossTypeReferenceCollisionIsRejected is the first reproduction. Creating
// an epic and an issue in one project previously gave both sequence 1 and both
// rendered "FAN-1". The second create must now fail.
func TestCrossTypeReferenceCollisionIsRejected(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)

	if _, err := env.Service.Create(env.Ctx, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "epic", Name: "Epic one", ActorID: actor,
	}); err != nil {
		t.Fatalf("create the epic: %v", err)
	}

	// The issue counter shares the epic's counter key, so the issue takes the
	// next value rather than colliding. Its reference must be unique.
	issueResult, err := env.Service.Create(env.Ctx, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Issue one", ActorID: actor,
	})
	if err != nil {
		t.Fatalf("create the issue: %v", err)
	}
	duplicates, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences: %v", err)
	}
	if len(duplicates) != 0 {
		t.Fatalf("an epic and an issue in one project produced duplicates %+v; the shared counter did not hold", duplicates)
	}
	_ = issueResult
}

// TestScopeRewriteCannotProduceADuplicate is the second reproduction. Moving a
// node into a scope whose counter already issued its number previously
// collided silently. It must now fail the write.
func TestScopeRewriteCannotProduceADuplicate(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	source := mustCreateScope(t, env, "project", "Src", workspace.ID, workspace.ID, actor)
	target := mustCreateScope(t, env, "project", "Dst", workspace.ID, workspace.ID, actor)

	resident, err := env.Service.Create(env.Ctx, service.CreateInput{
		ParentID: target.ID, ScopeID: target.ID, NodeTypeKey: "issue", Name: "Resident", ActorID: actor,
	})
	if err != nil {
		t.Fatalf("create the resident: %v", err)
	}
	migrant, err := env.Service.Create(env.Ctx, service.CreateInput{
		ParentID: source.ID, ScopeID: source.ID, NodeTypeKey: "issue", Name: "Migrant", ActorID: actor,
	})
	if err != nil {
		t.Fatalf("create the migrant: %v", err)
	}

	// Both hold value 1 in their own project. Moving the migrant into the
	// target project makes both render the same reference.
	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, migrant.View.ID)
	if err != nil || stored == nil {
		t.Fatalf("read the migrant: %v", err)
	}
	oldProps := cloneTestProps(stored.Props)
	stored.Props["scope_id"] = jsonStr(target.ID.String())
	stored.Props["parent_id"] = jsonStr(target.ID.String())

	keys := []node.ReferenceKey{{TemplateName: "reference", Encoded: "DST-1"}}
	err = env.Stores.Nodes.UpdateAtomic(env.Ctx, stored, nil, oldProps, nil, keys)
	if err == nil {
		t.Fatal("moving a node onto a taken reference: got nil error, want conflict")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("UpdateAtomic error = %v, want a domain.ErrConflict", err)
	}
	_ = resident
}
```

Add a `cloneTestProps` helper to the integration test helpers if one does not exist. The literal reference `DST-1` in the last assertion is the value the resident already holds; confirm it against the resident's rendered reference before asserting, and adjust if the seeded project identifier differs.

- [ ] **Step 2: Run the test to verify it passes**

Run: `make test-integration`

Expected: PASS, both tests.

- [ ] **Step 3: Confirm the tests are meaningful**

Temporarily comment out the `writeReferenceKeys` call in `CreateAtomic` and rerun. Expected: `TestCrossTypeReferenceCollisionIsRejected` still passes, because the shared counter alone prevents that collision, and `TestScopeRewriteCannotProduceADuplicate` fails. Restore the line. Record both outcomes; they show the counter and the key each carry part of the guarantee.

- [ ] **Step 4: Run the full suite**

Run: `make test-unit` then `make test-integration` then `make build`

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/test/integration/reference_uniqueness_test.go
git commit -S -m "Add reference uniqueness end-to-end regression (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

### Task 10: QA validation, blast-radius audit, and production rollout

**Files:** none. This task is operational.

**Interfaces:**
- Consumes: `reference.duplicates` (Task 6), `repair.reference_uniqueness` (Task 7).
- Produces: the recorded duplicate count for every project in every org, written onto TACK-342.

The eight confirmed duplicate references are all in the FAN project. Whether other projects carry the same condition is unknown. This task answers that.

- [ ] **Step 1: Merge after the review gates**

Confirm gpt-subagent, review-bugbot, and code-review approve, and CI is green, then merge to `main`.

- [ ] **Step 2: Deploy to QA and run detect**

Deploy the stack to `tack_qa` at the merged reference. Then run:

```bash
docker compose run --rm tack-ops ops reference.duplicates
```

Expected: the operation completes and reports a group count. Record it.

- [ ] **Step 3: Run the repair on QA and confirm convergence**

```bash
docker compose run --rm tack-ops ops repair.reference_uniqueness
docker compose run --rm tack-ops ops reference.duplicates
```

Expected: the second detect reports zero groups. Create a new issue through the MCP tools and rerun detect; expected zero groups, which proves the counter was seeded past the high-water mark.

- [ ] **Step 4: Recreate QA and confirm a fresh environment is clean**

Destroy and recreate `tack_qa`, run the first-boot sequence, then run detect. Expected: zero groups on a fresh seed.

- [ ] **Step 5: Run detect against production, read-only**

```bash
docker compose run --rm tack-ops ops reference.duplicates
```

Expected: at least the eight known FAN groups. Record every group, with its project, its rendered reference, and both node identifiers.

- [ ] **Step 6: Record the blast radius on TACK-342**

Post a comment on TACK-342 listing every duplicate group found in production, grouped by project. State explicitly whether any project other than FAN is affected. Correct the ticket description, which records four duplicates; the verified FAN count is eight.

- [ ] **Step 7: Run the repair against production**

Renumbering changes references people may have written down. Take a fresh backup and confirm the restore drill passes first, per `docs/runbooks/recovery.md`.

```bash
docker compose run --rm tack-ops ops repair.reference_uniqueness
```

Review the printed rename mapping. Then rerun with the execute gate once the mapping is confirmed correct.

- [ ] **Step 8: Verify production and record the mapping**

Run detect again; expected zero groups. Post the complete old-to-new rename mapping as a comment on TACK-342.

Confirm the four hand-created replacement issues are no longer needed: FAN-29, FAN-30, FAN-31, and FAN-32 each replace a reference that is now reachable. Decide with the operator whether to close them as superseded or keep them as the canonical record.

---

### Task 11: Remove the scan fallback

**Files:**
- Modify: `internal/adapters/mcp/tools/resolve_typed.go`
- Modify: `internal/adapters/mcp/tools/resolve_typed_test.go`

**Interfaces:**
- Consumes: the point-read path (Task 5), the completed backfill (Task 10).
- Produces: `resolveSequenceNodeID` resolving only through the reference key index.

Run this task only after Task 10 step 8 confirms detect reports zero groups in production. Until then the fallback is the only path for a node whose reference key was never written.

- [ ] **Step 1: Confirm the precondition**

Run `reference.duplicates` against production and QA. Expected: zero groups in both. Do not proceed otherwise.

- [ ] **Step 2: Delete the fallback**

Remove `scanSequenceCandidates` from `internal/adapters/mcp/tools/resolve_typed.go`, and reduce `resolveSequenceNodeID` to the point-read path:

```go
func (r *Resolver) resolveSequenceNodeID(ctx context.Context, input string, typeKeys []string) (uuid.UUID, error) {
	if _, _, err := ParseNodeIdentifier(input); err != nil {
		return uuid.Nil, fmt.Errorf("invalid node_id %q: must be a UUID or sequence reference like TACK-65: %w", input, domain.ErrInvalidArgument)
	}
	userID, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, fmt.Errorf("unauthenticated")
	}
	workspaces, err := r.WorkspacesForUser(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, workspace := range workspaces {
		if id, found := r.referenceKeyHolder(ctx, workspace.OrgID, typeKeys, input); found {
			return id, nil
		}
	}
	return uuid.Nil, fmt.Errorf("reference %q: %w", input, domain.ErrNotFound)
}
```

- [ ] **Step 3: Replace the fallback test**

Delete `TestResolveSequenceNodeIDFailsLoudOnDuplicates` from `internal/adapters/mcp/tools/resolve_typed_test.go`. It tested behavior that no longer exists, and a test for removed behavior is worse than no test.

Confirm `TestResolveTypedNodeIDSequenceStampsWorkspace` still covers the resolution path, updating its fake to serve the reference key rather than the property scan.

- [ ] **Step 4: Run everything**

Run: `make test-unit` then `make test-integration` then `make build`

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/mcp/tools/resolve_typed.go internal/adapters/mcp/tools/resolve_typed_test.go
git commit -S -m "Remove the reference scan fallback now the index is backfilled (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

---

## Self-review

**Spec coverage.** Every design section maps to a task.

| Spec section | Task |
| --- | --- |
| The declaration, part kinds | 1 |
| Counter derivation rule | 1 (derivation), 4 (use), 7 (seeding) |
| Enforcement, two key families | 2 (families and helpers), 3 (write paths) |
| Resolution, point read plus fallback | 5 |
| Seeded defaults | 8 |
| Repair, detect | 6 |
| Repair, renumber and `--keep` | 7 |
| No forwarding record | 7, by construction; the repair writes no forwarding entry |
| Testing, both reproductions | 9 |
| Testing, template-driven assertions | 1, 4 |
| Rollout steps 1 through 6 | 3, 6, 7, 10, 11 |
| Work breakdown item 6, blast-radius audit | 10 |

**Placeholder scan.** No TBD or TODO. Three steps record an expected intermediate failure rather than a pass, and each names the later task that resolves it: Task 6 step 4, Task 7 step 4, and Task 8 step 1. Task 9 step 1 flags one literal, `DST-1`, that must be confirmed against the seeded project identifier before asserting.

**Type consistency.** `node.ReferenceKey{TemplateName, Encoded}` is defined in Task 2 and used in Tasks 3, 4, 7, and 9. `ReferenceTemplate.Render`, `Parse`, and `CounterKey` are defined in Task 1 and used in Tasks 4, 6, and 7. `PrimaryReferenceTemplate` is defined in Task 1 and used in Tasks 4, 5, and 7. `SetReferenceKeys`, `LookupReference`, `AllocateSequenceByKey`, and `SeedSequenceByKey` are declared in Task 2 and used in Tasks 4, 5, and 7. `FindDuplicateReferences` and `DuplicateReference` are defined in Task 6 and used in Tasks 7, 9, and 10. `scopeReferencesForView` is defined in Task 6 and used in Task 7. `RepairReferenceOptions.Keep` accepts the same two values in Task 7's constants, its tests, and the spec.

**Signature changes.** `CreateAtomic` and `UpdateAtomic` both gain a `referenceKeys []ReferenceKey` parameter in Task 3. Task 3 step 5 requires fixing every call site. `Set` is unchanged by design.

**Known risk.** Task 8 changes the counter key shape for the four seeded work-item types. Any environment that already holds nodes of those types must run `repair.reference_uniqueness` before the next create, or the newly derived counter starts at zero and reissues values in use. Task 7's counter seeding runs first inside the repair for exactly this reason, and Task 10 sequences the repair immediately after deploy on both QA and production.
