package integration

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// identifierNodeCount is the number of allowed nodes. Half are containers
// under the caller's entry point, and each other node is the child of one
// container.
const identifierNodeCount = 12

// minimumScoreGap is the smallest relative score difference between two
// allowed nodes that are adjacent in rank. OpenSearch breaks an exact score
// tie by node ID, and new node IDs could then reorder tied nodes.
const minimumScoreGap = 0.01

// identifierTypeKeys are the container and nested type keys. Both builds use
// the same keys, and each result line then shows the same type key.
type identifierTypeKeys struct {
	Container, Nested string
}

// identifierCorpus is one build's allowed nodes in creation order, the
// caller's entry point, and every permission identifier of the build.
type identifierCorpus struct {
	Entry         uuid.UUID
	Allowed       []uuid.UUID
	PermissionIDs []uuid.UUID
}

func coverageWords() []string {
	return []string{"saffron", "pipeline", "granite", "harbor", "lantern", "meadow", "cobalt", "falcon", "juniper", "quartz", "willow", "ember"}
}

func reverseCoverageWords() []string {
	return []string{"orchid", "canyon", "velvet", "glacier", "maple", "beacon", "cedar", "tundra", "opal", "sparrow", "thistle", "marble"}
}

// identifierQueries returns one query per word list. Allowed node number
// contains 12-number words of the first query and number+1 words of the
// second query.
func identifierQueries() []string {
	return []string{strings.Join(coverageWords(), " "), strings.Join(reverseCoverageWords(), " ")}
}

func identifierText(number int) string {
	return strings.Join(coverageWords()[:identifierNodeCount-number], " ") + " " +
		strings.Join(reverseCoverageWords()[:number+1], " ")
}

// putIdentifierCorpus stores the allowed hierarchy under the caller's entry
// point, decoys under a sibling entry point of the same organization, and
// decoys in another organization. The decoys match every query word.
func putIdentifierCorpus(t *testing.T, fixture queryFixture, keys identifierTypeKeys) identifierCorpus {
	t.Helper()
	caller, sibling, foreign := fixture.Workspaces[0], fixture.Workspaces[1], otherOrganization(t, fixture)
	if sibling.OrgID != caller.OrgID {
		t.Fatalf("workspace %s is not in the caller's organization %s", sibling.Slug, caller.OrgID)
	}
	corpus := identifierCorpus{Entry: entryPoint(t, fixture, caller), Allowed: nil, PermissionIDs: nil}
	for _, workspace := range fixture.Workspaces {
		corpus.PermissionIDs = append(corpus.PermissionIDs, workspace.OrgID, entryPoint(t, fixture, workspace))
		for _, actor := range workspace.Actors {
			corpus.PermissionIDs = append(corpus.PermissionIDs, actor.UserID)
		}
	}
	container := putOpaqueKindWithTypeKey(t, fixture, caller.OrgID, keys.Container)
	nested := putNestedKind(t, fixture, caller.OrgID, keys.Nested, keys.Container)
	half := identifierNodeCount / 2
	for number := range identifierNodeCount {
		kind, parent := container, corpus.Entry
		if number >= half {
			kind, parent = nested, corpus.Allowed[number-half]
		}
		name := fmt.Sprintf("Identifier node %02d", number)
		corpus.Allowed = append(corpus.Allowed, putOpaqueNode(t, fixture, kind, parent, name, identifierText(number), "excluded"))
	}
	corpus.PermissionIDs = append(corpus.PermissionIDs, corpus.Allowed[:half]...)
	decoyText := strings.Repeat(strings.Join(identifierQueries(), " ")+" ", 2)
	foreignKind := putOpaqueKind(t, fixture, foreign.OrgID)
	for number := range half {
		name := fmt.Sprintf("Decoy %02d", number)
		putOpaqueNode(t, fixture, container, entryPoint(t, fixture, sibling), name, decoyText, "excluded")
		putOpaqueNode(t, fixture, foreignKind, entryPoint(t, fixture, foreign), name, decoyText, "excluded")
	}
	drainSearchWork(t, fixture.Worker, 2000)
	return corpus
}

// putNestedKind stores an opaque type with parentKey as the only permitted
// parent type.
func putNestedKind(t *testing.T, fixture queryFixture, orgID uuid.UUID, typeKey, parentKey string) opaqueKind {
	t.Helper()
	kind := putOpaqueKindWithTypeKey(t, fixture, orgID, typeKey)
	types, err := fixture.Stores.NodeTypes.List(t.Context(), orgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}
	for _, stored := range types {
		if stored.TypeKey != typeKey {
			continue
		}
		stored.CanLiveUnder = []string{parentKey}
		if err := fixture.Stores.NodeTypes.Set(t.Context(), stored); err != nil {
			t.Fatalf("store nested node type: %v", err)
		}
		return kind
	}
	t.Fatalf("node type %s is missing", typeKey)
	return kind
}

// identifierSearchLines follows every tack_search cursor and returns each
// result line in response order.
func identifierSearchLines(t *testing.T, harness *MCPHarness, query string) []string {
	t.Helper()
	var lines []string
	cursor := ""
	for range 100 {
		text, isError, err := rawSearchCall(harness, searchArguments(harness, query, cursor))
		if err != nil || isError {
			t.Fatalf("search %q: %v %s", query, err, text)
		}
		page, err := parseSearchPage(text)
		if err != nil {
			t.Fatalf("parse search %q: %v", query, err)
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "- ") && strings.Contains(line, "Raw id: `") {
				lines = append(lines, line)
			}
		}
		if page.Complete {
			return lines
		}
		cursor = page.Cursor
	}
	t.Fatalf("search %q did not complete within 100 pages", query)
	return nil
}

// requireSeparatedScores requires the raw ranker to score every allowed node
// and to separate adjacent allowed nodes by minimumScoreGap.
func requireSeparatedScores(t *testing.T, fixture queryFixture, filter searchdomain.AccessFilter, query string, allowed []uuid.UUID) {
	t.Helper()
	scores := rankedScores(t, fixture, filter, query)
	ranked := make([]float64, 0, len(allowed))
	for _, id := range allowed {
		score, found := scores[id]
		if !found {
			t.Fatalf("raw ranking of %q omits allowed node %s", query, id)
		}
		ranked = append(ranked, score)
	}
	slices.Sort(ranked)
	for number := 1; number < len(ranked); number++ {
		if ranked[number]-ranked[number-1] < minimumScoreGap*ranked[number] {
			t.Fatalf("query %q scores allowed nodes %v within %.0f percent of each other", query, ranked, minimumScoreGap*100)
		}
	}
}
