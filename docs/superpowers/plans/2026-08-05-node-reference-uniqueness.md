# Implement node reference uniqueness

Read each ticket before implementation. A delivery unit combines one ticket,
one delegated implementation, and one pull request in a Graphite stack. The
numbered tasks provide reference material by responsibility rather than an
execution order. Ticket scope overrides task commit instructions and requires
production callers with their implementations. Delegated implementers leave
changes uncommitted; the host stages and creates the Graphite branch.

Enforce unique human-readable references such as `FAN-13` in FoundationDB write
transactions. NodeType stores ordered reference templates. One declaration
renders and parses references and derives uniqueness and sequence-counter keys.
A forward index maps each rendered value to its node; a reverse index records
that node's keys. Generated counters omit only the template's generated part
and use the same uniqueness domain.

Follow the [reference uniqueness design](../specs/2026-08-05-node-reference-uniqueness-design.md).
Use Go, the FoundationDB tuple layer, UUIDv7 from `github.com/google/uuid`, the
internal/ops registry, `make build`, and `make test-integration`.

## Constraints and delivery order

Require all nine build checks to print `ok`: vet, lint-golangci, lint-format,
lint-gocyclo, lint-deadcode, staticcheck-extra, govulncheck, cgo-stub, and
platform-stub. Do not invoke go build or treat a partial summary as success.
Deadcode analyzes calls from main rather than tests; every delivery unit must
include a production caller for each new function.

`staticcheck/wrapped_error.go` rejects functions returning fmt.Errorf with %w
or %v unless that function also invokes slog.Error or slog.Warn. Build the
wrapped error, log a noun.verb message, and return it. The codec exemption
requires both a matching name and standard-library-shaped signature, such as
Marshal* with `() ([]byte, error)`. Do not rename functions to avoid it.

- Interpret NodeType metadata rather than concrete issue/epic type keys.
- Do not add engine reference shapes, implicit parts, fallback templates, or
  concrete property identifiers such as sequence outside seed data. A type
  without templates must remain unconstrained and must not render a reference.
  Concrete examples below are deployment fixtures rather than engine defaults.
- Resolve metadata in the service, not storage, matching indexedProps in
  domain/node/repository.go:84-86.
- Keep files at most 200 lines. Use one FDB transaction through
  EntityRepository.CreateAtomic, Set, or UpdateAtomic for writes. Do not add
  multistep creates or cross-database transactions.
- Use slog attributes and noun.verb messages, with Info for ordinary work,
  Debug for detail, and Error for failure. Include context and identifiers
  in every error, with logging for wrapped errors.
- Use full domain terms, package comments, and comments for exported types
  and non-obvious fields. Do not add compatibility shims, unused exports,
  re-exports, or comments on unrelated code.
- Integration tests use the integration build tag and skip without their DSN,
  matching internal/audit/chain_append_integration_test.go.
- Register lowercase dot-separated maintenance operations with ops.Register.
- Validate migrations, seeds, backfills, and restores on disposable QA before
  production.
- The direct go test commands below support quick unit checks while the trunk
  make test-unit runner incorrectly treats /usr/local/go/bin/go as a package.
  Every delivery unit must still pass containerized make test-integration.

Use this ticket order:

| Delivery unit | Ticket | Reference tasks |
|---|---|---|
| Templates, uniqueness index, create enforcement | TACK-366 | 1, 2, 3, 4 |
| Derived counter keys | TACK-367 | 1 CounterKey, Task 2 counter methods; Task 4 allocateCreateSequence |
| Update/delete key replacement | TACK-374 | 3 |
| Point reads and parsing | TACK-380 | 1 Parse, 5 |
| Duplicate detection | TACK-382 | 6 |
| Repair operation | TACK-385 | 7 |
| Seed templates | TACK-386 | 8 |
| End-to-end regressions | TACK-387 | 9 |
| QA validation | TACK-389 | 10 steps 1-4 |
| Production read-only audit | TACK-390 | 10 steps 5-6 |
| Production repair | TACK-391 | 10 steps 7-8 |
| Remove scan fallback | TACK-392 | 11 |

## Task 1: Define reference templates

Create domain/node/reference_template.go and its test. Update types.go with
ReferenceTemplates and PrimaryReferenceTemplate. The domain types implement
Render, Parse, and CounterKey without storage.

- [ ] Add the failing tests:

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

// issueTemplate omits node type from the scoped sequence reference.
// An epic and issue therefore cannot both claim FAN-1 in one scope.
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
		t.Fatal("Render without a scope reference returned nil error, want failure")
	}
}

func TestReferenceTemplateRenderMissingPropertyFails(t *testing.T) {
	_, err := issueTemplate().Render(ReferenceRenderInput{
		NodeTypeKey: "issue",
		Props:       map[string]json.RawMessage{},
		ScopeRefs:   map[string]string{FeatureIsScope: "FAN"},
	})
	if err == nil {
		t.Fatal("Render without a sequence property returned nil error, want failure")
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

// TestReferenceTemplateParsesHyphenatedScope preserves separators inside scopes.
// The final property consumes only the final segment.
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
		t.Fatal("Parse without a separator returned nil error, want failure")
	}
}

// TestReferenceTemplateCounterKeyExcludesGenerated verifies shared counters
// for templates that omit node type and generate only the final property.
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
		t.Fatalf("PrimaryReferenceTemplate without templates = %+v, want nil", got)
	}
}
```

- [ ] Run `go test ./internal/domain/node/ -run 'TestReferenceTemplate|TestPrimaryReferenceTemplate' -v`
  and require undefined ReferenceTemplate compilation failure.
- [ ] Implement reference_template.go:

```go
package node

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ReferencePartKind selects a component of a reference template.
type ReferencePartKind string

const (
	// ReferencePartScopeRef renders the nearest ancestor reference for Value's feature.
	ReferencePartScopeRef ReferencePartKind = "scope_ref"
	// ReferencePartProperty renders the property specified by Value.
	ReferencePartProperty ReferencePartKind = "property"
	// ReferencePartNodeType renders the node's TypeKey and ignores Value.
	ReferencePartNodeType ReferencePartKind = "node_type"
	// ReferencePartLiteral renders Value verbatim and delimits parsed segments.
	ReferencePartLiteral ReferencePartKind = "literal"
)

// ReferencePart defines one reference component.
type ReferencePart struct {
	Kind ReferencePartKind `json:"kind"`
	// Value stores the required feature, property, or literal text.
	// ReferencePartNodeType ignores Value.
	Value string `json:"value,omitempty"`
}

// ReferenceTemplate constrains its rendered string within one organization.
// Two nodes cannot render an identical value under the same template name.
type ReferenceTemplate struct {
	// Name identifies the template in type metadata, storage keys, and errors.
	Name string `json:"name"`
	// Parts specifies render order.
	Parts []ReferencePart `json:"parts"`
	// IsPrimary selects the human-readable reference; at most one template sets it.
	IsPrimary bool `json:"is_primary,omitempty"`
	// Generated identifies the create-time generated property; empty requires supplied values.
	Generated string `json:"generated,omitempty"`
}

// ReferenceRenderInput supplies property values and nearest ancestor references.
// ScopeRefs maps each declared feature to that ancestor's rendered reference.
type ReferenceRenderInput struct {
	NodeTypeKey string
	Props       map[string]json.RawMessage
	ScopeRefs   map[string]string
}

// ReferenceParseResult stores the variable components recovered from a reference.
type ReferenceParseResult struct {
	ScopeRefs   map[string]string
	Props       map[string]string
	NodeTypeKey string
}

// Render rejects missing part values rather than creating ambiguous partial references.
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

// CounterKey renders all parts except the generated property for sequence allocation.
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
			return "", fmt.Errorf("scope reference part %q: an ancestor reference for the feature does not exist", part.Value)
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

// referencePropertyString renders JSON strings or numbers as reference text.
// Integral sequence 13 renders as 13 without a decimal point.
func referencePropertyString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("property does not have a value")
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

// Parse recovers variable parts separated by literals. The final variable
// consumes the remainder, preserving separators within a leading scope reference.
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

// consumeReferenceSegment separates one variable value from its following literal.
func consumeReferenceSegment(parts []ReferencePart, index int, remaining string) (string, string, error) {
	delimiter := nextReferenceLiteral(parts, index)
	if delimiter == "" {
		if remaining == "" {
			return "", "", fmt.Errorf("the final part does not have text")
		}
		return remaining, "", nil
	}
	cut := strings.LastIndex(remaining, delimiter)
	if cut < 0 {
		return "", "", fmt.Errorf("separator %q not found", delimiter)
	}
	if cut == 0 {
		return "", "", fmt.Errorf("text before separator %q does not exist", delimiter)
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

- [ ] Add this field after Reference in types.go:146 and the accessor after
  BuildTypeIndex:

```go
	// ReferenceTemplates constrains rendered strings by template and organization.
	// At most one template selects IsPrimary for the human-readable reference.
	ReferenceTemplates []ReferenceTemplate `json:"reference_templates,omitempty"`
```

```go
// PrimaryReferenceTemplate returns the primary declaration or nil when absent.
func (nt *NodeType) PrimaryReferenceTemplate() *ReferenceTemplate {
	for index := range nt.ReferenceTemplates {
		if nt.ReferenceTemplates[index].IsPrimary {
			return &nt.ReferenceTemplates[index]
		}
	}
	return nil
}
```

- [ ] Repeat the domain test command and require all eleven tests to pass.
  Run make build, then commit:

```bash
git add internal/domain/node/reference_template.go internal/domain/node/reference_template_test.go internal/domain/node/types.go
git commit -S -m "Add reference key templates to NodeType metadata (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 2: Store uniqueness claims and sequence counters

Modify foundationdb/keys.go, node.go, and domain/node/repository.go. Create
foundationdb/node_reference.go and integration/reference_store_test.go.
Storage accepts rendered strings without reading metadata.

- [ ] Add the reference-store regressions:

```go
package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// TestReferenceKeyRejectsASecondHolder verifies one node per template/reference.
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
		t.Fatal("SetReferenceKeys accepted a duplicate node reference, want conflict")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("SetReferenceKeys error = %v, want a domain.ErrConflict", err)
	}
}

// TestReferenceKeyIsIdempotentForItsHolder verifies repeated claims by one node.
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

// TestReferenceKeyMovesWithTheNode verifies reuse after reference replacement.
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

// TestLookupReferenceMissReturnsNil verifies a missing claim returns nil UUID
// without error, permitting legacy resolution fallback.
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

Add errors and service imports. Require make test-integration to fail for
undefined SetReferenceKeys before implementation.

- [ ] Add key families after keyIdempotency, then packers after idempotencyKey:

```go
	// keyNodeReference maps each constrained reference to its node UUID.
	// (node_reference, orgID, templateName, encodedKey) -> nodeID
	keyNodeReference = "node_reference"

	// keyNodeReferenceOwned records prior node claims for update/delete.
	// Storage reads this reverse index without interpreting NodeType metadata.
	// (node_reference_owned, orgID, nodeID, templateName) -> encodedKey
	keyNodeReferenceOwned = "node_reference_owned"
```

```go
// nodeReferenceKey packs the forward uniqueness key.
func nodeReferenceKey(orgID uuid.UUID, templateName, encoded string) []byte {
	return withPrefix(tuple.Tuple{keyNodeReference, orgID.String(), templateName, encoded}.Pack())
}

// nodeReferenceOwnedKey packs one node's reverse template entry.
func nodeReferenceOwnedKey(orgID, nodeID uuid.UUID, templateName string) []byte {
	return withPrefix(tuple.Tuple{keyNodeReferenceOwned, orgID.String(), nodeID.String(), templateName}.Pack())
}

// nodeReferenceOwnedPrefix selects every reverse reference entry for a node.
func nodeReferenceOwnedPrefix(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeReferenceOwned, orgID.String(), nodeID.String()}.Pack())
}
```

- [ ] Define ReferenceKey before NodeRepository and add repository methods
  after AllocateSequence:

```go
// ReferenceKey pairs a template name with its rendered value.
// The service interprets metadata; storage persists the rendered claim.
type ReferenceKey struct {
	// TemplateName identifies ReferenceTemplate.Name.
	TemplateName string
	// Encoded stores the rendered reference, such as FAN-13.
	Encoded string
}
```

```go
	// SetReferenceKeys replaces all node claims and rejects another node's claim.
	// An empty slice releases every reference key for the node.
	SetReferenceKeys(ctx context.Context, orgID, nodeID uuid.UUID, keys []ReferenceKey) error

	// LookupReference returns the claiming node or nil UUID without error on a miss.
	LookupReference(ctx context.Context, orgID uuid.UUID, templateName, encoded string) (uuid.UUID, error)

	// AllocateSequenceByKey atomically increments the counter derived from a
	// reference template without its generated part.
	AllocateSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string) (int64, error)

	// SeedSequenceByKey raises the counter to at least value without lowering it.
	// Repair initializes derived counters from the scope's maximum existing value.
	SeedSequenceByKey(ctx context.Context, orgID uuid.UUID, counterKey string, value int64) error
```

- [ ] Implement transactional claims and lookup:

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

// writeReferenceKeys replaces claims inside the caller's transaction.
// A different claiming node causes the entire transaction to fail.
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
				"reference %q for template %q already identifies node %s: %w",
				key.Encoded, key.TemplateName, string(existing), domain.ErrConflict,
			)
		}
		tr.Set(forward, []byte(nodeID.String()))
		tr.Set(fdb.Key(nodeReferenceOwnedKey(orgID, nodeID, key.TemplateName)), []byte(key.Encoded))
	}
	return nil
}

// clearReferenceKeys reads the reverse index and releases claims transactionally.
// It does not read NodeType metadata.
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

// SetReferenceKeys replaces every reference claim for the node.
func (s *NodeStore) SetReferenceKeys(ctx context.Context, orgID, nodeID uuid.UUID, keys []node.ReferenceKey) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.set_reference_keys")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		return nil, writeReferenceKeys(tr, orgID, nodeID, keys)
	})
	return
}

// LookupReference resolves one template/reference claim to its node ID.
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
		return uuid.Nil, fmt.Errorf("reference %q maps to invalid node ID %q: %w", encoded, raw, parseErr)
	}
	return held, nil
}
```

- [ ] Add the counter methods after AllocateSequence in node.go and the
  sequenceByKeyKey packer after sequenceKey:

```go
// AllocateSequenceByKey increments the template-derived counter atomically.
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

// SeedSequenceByKey raises the counter without decreasing an existing value.
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

```go
// sequenceByKeyKey selects a rendered-template counter rather than scope/type.
func sequenceByKeyKey(orgID uuid.UUID, counterKey string) []byte {
	return withPrefix(tuple.Tuple{keySequence, orgID.String(), counterKey}.Pack())
}
```

- [ ] Require the four reference-store tests and make build to pass. Commit:

```bash
git add internal/adapters/foundationdb/keys.go internal/adapters/foundationdb/node_reference.go internal/adapters/foundationdb/node.go internal/domain/node/repository.go internal/test/integration/reference_store_test.go
git commit -S -m "Add node reference key families and uniqueness enforcement helpers (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 3: Enforce claims on create, update, and delete

Update foundationdb/node.go and domain/node/repository.go. Extend the store
regression. Preserve Set's existing caller-reconciled index contract.

- [ ] Add these tests and encoding/json import. Require make test-integration
  compilation failure because CreateAtomic still accepts six rather than seven
  arguments:

```go
// TestCreateAtomicRejectsADuplicateReference verifies atomic duplicate refusal.
// Rejection must not persist any part of the new node.
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
		t.Fatal("CreateAtomic with a taken reference returned nil error, want conflict")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateAtomic error = %v, want a domain.ErrConflict", err)
	}

	stored, getErr := env.Stores.Nodes.Get(env.Ctx, env.OrgID, intruder.ID)
	if getErr != nil {
		t.Fatalf("Get the rejected node: %v", getErr)
	}
	if stored != nil {
		t.Fatal("the rejected create persisted a node; the transaction did not roll back")
	}
}

// TestDeleteReleasesTheReference verifies reference reuse after node deletion.
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
		t.Fatalf("FAN-55 still identifies %s after delete, want a released reference", holder)
	}
}
```

- [ ] Replace repository declarations:

```go
	// UpdateAtomic writes node/view records and reconciles indexedProps against
	// oldProps in one transaction. Nil referenceKeys preserves existing claims;
	// a non-nil slice replaces them, and a non-nil empty slice releases them all.
	// A duplicate claim rolls back all changes.
	UpdateAtomic(
		ctx context.Context,
		n *Node,
		view *NodeView,
		oldProps map[string]json.RawMessage,
		indexedProps []string,
		referenceKeys []ReferenceKey,
		relationshipChanges ...RelationshipChanges,
	) error

	// CreateAtomic writes the node, relationships, and reference claims together.
	// NodeService.Create allocates sequences, supplies Props, and renders claims.
	// indexedProps selects secondary property indexes and referenceKeys supplies
	// uniqueness claims; storage does not read their metadata registries.
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

- [ ] Implement the update transaction:

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
		if referenceKeys != nil {
			if err := writeReferenceKeys(tr, n.OrgID, n.ID, referenceKeys); err != nil {
				return nil, err
			}
		}
		if err := applyRelationshipChanges(ctx, tr, relationshipChanges); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return
}
```

Add referenceKeys before idempotency in CreateAtomic and invoke uniqueness
after property indexes and before relationships:

```go
		// Reject duplicate reference claims before committing the create.
		if err := writeReferenceKeys(tr, n.OrgID, n.ID, referenceKeys); err != nil {
			return nil, err
		}
```

After property-index clearing in Delete's transaction, add:

```go
		// Release prior claims through the reverse index without metadata reads.
		if err := clearReferenceKeys(tr, orgID, nodeID); err != nil {
			return nil, err
		}
```

- [ ] Run make build. The current callers already accept referenceKeys under the
  [NodeRepository contract](../../../internal/domain/node/repository.go).
  Task 4 renders service-create keys from metadata. Create callers must supply
  those keys for templated types. Update callers may pass nil only when preserving
  existing claims; changed references require the complete replacement key set.
  Require both new integration tests to pass. Commit:

```bash
git add internal/adapters/foundationdb/node.go internal/domain/node/repository.go internal/test/integration/reference_store_test.go
git commit -S -m "Enforce reference keys in CreateAtomic, UpdateAtomic, and Delete (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 4: Render keys and allocate generated values

Create service/node_reference_keys.go and its test. Update node_create_prepare.go,
node_create.go, and node_create_idempotency.go. Resolve scope ancestor features
and their own direct-property references, such as project FAN.

The declared service APIs are
`(*NodeService).referenceKeysFor(ctx context.Context, orgID uuid.UUID, nt *node.NodeType, nodeTypeKey string, scopeID uuid.UUID, props map[string]json.RawMessage) ([]node.ReferenceKey, error)`
and `(*NodeService).scopeRefsFor(ctx context.Context, orgID, scopeID uuid.UUID) (map[string]string, error)`.

- [ ] Add the template-property contract test:

```go
package service

import (
	"encoding/json"
	"testing"

	"goodkind.io/tack/internal/domain/node"
)

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

Run `go test ./internal/service/ -run TestAllocateCreateSequenceUsesTheTemplateProperty -v`.
The domain assertions should pass before integration, while changed signatures
can cause an intermediate compilation failure. Record the actual result.

- [ ] Implement the renderer and scope lookup:

```go
package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const maxScopeWalkDepth = 32

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

- [ ] Replace allocateCreateSequence in node_create_prepare.go:

```go
// The template defines the counter domain instead of a type/property naming convention.
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

After indexedProps in node_create.go:97-100, add:

```go
	referenceKeys, err := s.referenceKeysFor(ctx, orgID, nt, in.ScopeID, props)
	if err != nil {
		return nil, err
	}
```

Pass referenceKeys after indexedProps through createNodeAtomic in
node_create_idempotency.go to CreateAtomic.

- [ ] Run `go test ./internal/service/ -v`, make build, and make test-integration.
  Require success. Before Task 8, seed types lack templates and both helpers
  retain their empty-template behavior. Commit:

```bash
git add internal/service/node_reference_keys.go internal/service/node_reference_keys_test.go internal/service/node_create_prepare.go internal/service/node_create.go internal/service/node_create_idempotency.go
git commit -S -m "Render reference keys and allocate from the template-derived counter (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 5: Resolve claims and reject ambiguous legacy references

Update tools/resolve_typed.go, resolve_reference_helpers.go:99-109, and tests.
Use LookupReference before the legacy candidate scan and require uniqueMatch
for fallback results.

- [ ] Add the ambiguity regression:

```go
// TestResolveSequenceNodeIDFailsLoudOnDuplicates requires both candidate IDs
// when legacy nodes render the same reference without uniqueness-index entries.
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
		t.Fatal("ResolveTypedNodeID accepted a duplicated reference, want ambiguity")
	}
	if !strings.Contains(err.Error(), "matched multiple nodes") {
		t.Fatalf("error = %v, want it to report multiple matches", err)
	}
	if !strings.Contains(err.Error(), firstID.String()) || !strings.Contains(err.Error(), secondID.String()) {
		t.Fatalf("error = %v, want both candidate UUIDs for operator recovery", err)
	}
}
```

- [ ] Run `go test ./internal/adapters/mcp/tools/ -run TestResolveSequenceNodeIDFailsLoudOnDuplicates -v`
  and require failure while resolution returns the first candidate.
- [ ] Replace the resolver and add errors import:

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

// scanSequenceCandidates collects legacy candidates and rejects ambiguous results.
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

Replace uniqueMatch and add sort import. Preserve the message fragment
`matched multiple nodes`, which the regression asserts:

```go
// uniqueMatch returns one candidate or an error listing all ambiguous node UUIDs.
// Callers can use those UUIDs when the rendered reference is ambiguous.
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

- [ ] Require `go test ./internal/adapters/mcp/tools/ -v` and make build to
  pass, then commit:

```bash
git add internal/adapters/mcp/tools/resolve_typed.go internal/adapters/mcp/tools/resolve_reference_helpers.go internal/adapters/mcp/tools/resolve_typed_test.go
git commit -S -m "Resolve references by point read and fail loud on legacy duplicates (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 6: Detect duplicate references

Create ops/reference_duplicates.go and integration/reference_duplicates_test.go.
Export FindDuplicateReferences for Task 7 and register reference.duplicates
through the existing ops.Register/Env API at ops.go:39-54.

- [ ] Add the failing test:

```go
package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/ops"
)

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
		t.Fatalf("group %q contains %d nodes, want 2", group.Encoded, len(group.NodeIDs))
	}
	held := map[uuid.UUID]bool{group.NodeIDs[0]: true, group.NodeIDs[1]: true}
	if !held[epic.ID] || !held[issue.ID] {
		t.Fatalf("group %q contains %v, want the epic %s and the issue %s", group.Encoded, group.NodeIDs, epic.ID, issue.ID)
	}
}
```

Add Ops *ops.Env to TestEnv using its existing stores and pool. Require
make test-integration to fail for undefined ops.FindDuplicateReferences.

- [ ] Implement detection:

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
		Description: "Report references that identify multiple nodes across organizations and node types without changing data.",
		Run:         runReferenceDuplicates,
	})
}

// DuplicateReference identifies one rendered value shared by multiple nodes.
type DuplicateReference struct {
	OrgID        uuid.UUID
	TemplateName string
	// Encoded stores the rendered reference, such as FAN-13.
	Encoded string
	// NodeIDs lists claiming nodes in stable order.
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

// FindDuplicateReferences groups rendered template values across node types.
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

// scopeReferencesForView uses the service's ancestor-reference interpretation.
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

Remove the trailing json.Marshal reference and encoding/json import if unused.
The detection test still fails without Task 8's templates. Temporarily declare
issue/epic templates in test setup to verify detection, remove that local setup,
and record the expected intermediate result. Task 8 reruns unchanged tests.

- [ ] Run make build and commit:

```bash
git add internal/ops/reference_duplicates.go internal/test/integration/reference_duplicates_test.go
git commit -S -m "Add reference.duplicates detect operation (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 7: Repair duplicates and backfill claims

Create ops/repair_reference_uniqueness.go and integration/reference_repair_test.go.
Keep accepts oldest or newest and defaults to oldest. UUIDv7 order supplies
creation order. This is repair policy rather than a storage rule. Seed all
counters to existing maxima before allocating replacement values; then renumber
duplicates and write claims for every templated node, including older nodes.

- [ ] Add the failing tests and require undefined RepairReferenceUniqueness:

```go
package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/ops"
)

// TestRepairReferenceUniquenessRenumbersAndHoldsTheLine verifies renumbering,
// retained identity, seeded counters, and uniqueness after another create.
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
		t.Fatalf("duplicates after repair = %+v, want an empty result", remaining)
	}

	// A new create must not reuse an existing number.
	fresh := mustCreate(t, env, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Fresh", ActorID: actor,
	})
	after, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences after a fresh create: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("a create after repair produced duplicates %+v; the counter did not exceed the existing maximum", after)
	}
	_ = fresh
}

// TestRepairReferenceUniquenessDryRunChangesNothing verifies nonmutating reports.
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
		t.Fatalf("duplicates after a dry run = %d, want the original 1 unchanged", len(remaining))
	}
}
```

- [ ] Implement the operation:

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

// keepOldest and keepNewest select repair retention policy, not storage behavior.
const (
	keepOldest = "oldest"
	keepNewest = "newest"
)

func init() {
	Register(Operation{
		Name:        "repair.reference_uniqueness",
		Description: "Seed reference counters, renumber duplicates, and backfill uniqueness keys. Preview does not write until the operator enables execution.",
		Run:         runRepairReferenceUniqueness,
	})
}

// RepairReferenceOptions configures one repair run.
type RepairReferenceOptions struct {
	// Execute enables writes; false only reports planned changes.
	Execute bool
	// Keep selects oldest or newest retention; an empty value uses oldest.
	Keep string
}

// ReferenceRename records one reassignment.
type ReferenceRename struct {
	NodeID uuid.UUID
	From   string
	To     string
}

// RepairReferenceReport summarizes one repair run.
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

// RepairReferenceUniqueness seeds counters before renumbering and claim backfill.
// Newly derived counters start at zero and otherwise reissue existing values.
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

// seedReferenceCounters initializes each counter from its scope's maximum value.
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

// renumberDuplicateGroup preserves the selected node and renumbers the others.
// UUIDv7 ordering supplies creation order.
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
		return ReferenceRename{}, fmt.Errorf("node type %q does not declare a generated reference part; repair cannot renumber node %s", nodeType.TypeKey, nodeID)
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

// writeAllReferenceKeys backfills uniqueness claims for all templated nodes.
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

// viewFromRepairedNode reconstructs the read view using normal node fields.
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

- [ ] Run make test-integration. Before Task 8, require successful compilation
  and record expected missing-template failures; Task 8 reruns both tests.
  Run make build and commit:

```bash
git add internal/ops/repair_reference_uniqueness.go internal/test/integration/reference_repair_test.go
git commit -S -m "Add repair.reference_uniqueness renumber and backfill operation (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 8: Seed explicit templates

Only seed.go defines the deployment's issue, epic, cycle, and module templates.
An organization may change or remove them; the engine must not provide a
fallback or implicit part. Omitting node_type shares numbering across types.
Including node_type permits independent numbering with the same interpreter.

- [ ] Run make test-integration and record Task 6/7 missing-template failures.
  Add the constructor above seed type declarations:

```go
// scopedSequenceTemplate defines this deployment's shared work-item numbering.
// Organizations may change or remove this seed data; absent templates do not constrain.
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

Locate issue, epic, cycle, and module declarations by FeatureHasSequenceID
rather than slug. Add:

```go
			ReferenceTemplates: scopedSequenceTemplate(),
```

- [ ] Require Task 6 and Task 7 tests to pass unchanged under
  make test-integration. Run make build and commit:

```bash
git add internal/service/seed.go
git commit -S -m "Seed a shared scoped-sequence reference template on work-item types (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 9: Reproduce both collision mechanisms

Create integration/reference_uniqueness_test.go. Use real create/update paths
rather than synthetic persisted fixtures. This task does not introduce an API.

- [ ] Add the regressions:

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

// TestCrossTypeReferenceCollisionIsRejected verifies shared numbering across types.
// Previously an epic and issue each received sequence 1 and rendered FAN-1.
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

	// The shared counter assigns the next value to the issue.
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
		t.Fatalf("an epic and issue produced duplicates %+v; the shared counter did not prevent collisions", duplicates)
	}
	_ = issueResult
}

// TestScopeRewriteCannotProduceADuplicate verifies refusal when scope reassignment
// would duplicate a number already allocated in the destination.
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

	// Both nodes use sequence 1 in separate projects. A destination reassignment
	// would make their rendered references identical.
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
		t.Fatal("scope reassignment accepted a taken reference, want conflict")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("UpdateAtomic error = %v, want a domain.ErrConflict", err)
	}
	_ = resident
}
```

Add cloneTestProps if absent. Confirm DST-1 against the resident's actual
rendered reference and adjust the fixture assertion if its seeded project
identifier differs. Require both tests to pass under make test-integration.
Temporarily omit CreateAtomic's writeReferenceKeys invocation. The cross-type
counter regression should still pass, while the scope-rewrite regression must
fail. Restore the invocation and record both results. This verifies distinct
counter and uniqueness-index responsibilities.

- [ ] Run make test-unit, make test-integration, and make build. Require success
  and commit:

```bash
git add internal/test/integration/reference_uniqueness_test.go
git commit -S -m "Add reference uniqueness end-to-end regression (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

## Task 10: Validate and repair deployment data

Eight confirmed FAN duplicate references require repair. Determine whether
other projects or organizations also contain duplicates and record the
complete counts on TACK-342. Its existing description incorrectly reports four.

1. Require gpt-subagent, review-bugbot, and code-review approval and passing CI
   before merging main.
2. Deploy the merged ref to tack_qa and record the detection count:

```bash
docker compose run --rm tack-ops ops reference.duplicates
```

3. Repair QA and detect again:

```bash
docker compose run --rm tack-ops ops repair.reference_uniqueness
docker compose run --rm tack-ops ops reference.duplicates
```

Require zero duplicate groups. Create an issue through MCP and require zero
groups again to verify seeded counters.

4. Destroy and recreate tack_qa, run first boot, and require zero groups from
   its fresh seed.
5. Run read-only production detection:

```bash
docker compose run --rm tack-ops ops reference.duplicates
```

Require at least the eight known FAN groups. Record each project, rendered
reference, and both node identifiers.

6. Attach every group to TACK-342 by project and state whether any non-FAN
   project is affected. Correct the description's four-duplicate count to the
   verified eight FAN groups.
7. Renumbering changes references users may have saved. Take a fresh backup
   and require a passing restore drill under the recovery runbook first:

```bash
docker compose run --rm tack-ops ops repair.reference_uniqueness
```

Review the preview mapping, confirm it, and rerun with the execute gate.

8. Require zero production groups and attach the complete old/new mapping.
   FAN-29, FAN-30, FAN-31, and FAN-32 manually replaced formerly ambiguous
   references. Resolve their canonical status with the operator: close as
   superseded or retain as the authoritative records.

Task 8 changes counter-key layout for four seeded types. Existing environments
must run repair.reference_uniqueness immediately after deployment, before the
next create. Its first step seeds counters to existing maximum values; otherwise a
new zero-based counter reissues occupied references.

## Task 11: Remove legacy scanning after backfill

Require Task 10 step 8 and zero reference.duplicates groups on both production
and QA before proceeding. Until then, legacy nodes without index entries still
require the scan. Update resolve_typed.go and its tests.

- [ ] Delete scanSequenceCandidates and replace the sequence resolver:

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

- [ ] Delete TestResolveSequenceNodeIDFailsLoudOnDuplicates, which tests the
  removed scan. Preserve TestResolveTypedNodeIDSequenceStampsWorkspace by
  updating its existing fake to supply reference-index claims rather than scans.
- [ ] Require make test-unit, make test-integration, and make build success.
  Commit:

```bash
git add internal/adapters/mcp/tools/resolve_typed.go internal/adapters/mcp/tools/resolve_typed_test.go
git commit -S -m "Remove the reference scan fallback now the index is backfilled (TACK-342)

Co-authored-by: Claude <noreply@anthropic.com>"
```

Tasks 1 and 2 define template and key interfaces; Tasks 3, 4, 5, 7, and 9 use
the same ReferenceKey declarations. Task 3 requires every caller to accept
referenceKeys while Set preserves its externally reconciled contract.
Tasks 6 and 7 intentionally record missing-template failures before Task 8
seeds declarations. The repair does not create forwarding records. Detection,
renumbering, UUID resolution, and final removal retain the same template domain.
