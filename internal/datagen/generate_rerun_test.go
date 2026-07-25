package datagen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGeneratorRerunUsesIdempotentCreateRawIDs(t *testing.T) {
	t.Parallel()
	fake := &rerunMCP{nodes: make(map[string]map[string]rerunNode)}

	first := runGeneratorPass(t, fake)
	uniqueAfterFirst := fake.uniqueNodeCount()
	probeCreatesAfterFirst := fake.newProbeCreates
	if probeCreatesAfterFirst != 1 {
		t.Fatalf("first pass deletion probe creates = %d, want 1", probeCreatesAfterFirst)
	}
	second := runGeneratorPass(t, fake)

	if second.Created != 0 {
		t.Fatalf("second pass created = %d, want 0", second.Created)
	}
	if second.Reused != first.Created {
		t.Fatalf("second pass reused = %d, want %d", second.Reused, first.Created)
	}
	if got := fake.uniqueNodeCount(); got != uniqueAfterFirst {
		t.Fatalf("unique nodes after rerun = %d, want %d", got, uniqueAfterFirst)
	}
	if fake.newProbeCreates != probeCreatesAfterFirst {
		t.Fatalf(
			"new deletion probes after rerun = %d, want %d",
			fake.newProbeCreates,
			probeCreatesAfterFirst,
		)
	}
	if len(fake.invalidGetReferences) != 0 {
		t.Fatalf("second pass used rendered references in get calls: %v", fake.invalidGetReferences)
	}
}

func runGeneratorPass(t *testing.T, handler http.Handler) Summary {
	t.Helper()
	const seed int64 = 245
	content, err := NewContent(t.Context(), seed)
	if err != nil {
		t.Fatalf("NewContent() error = %v", err)
	}
	scale, err := ParseScale("small")
	if err != nil {
		t.Fatalf("ParseScale() error = %v", err)
	}
	identities := Identities{Workspaces: []WorkspaceIdentity{{
		OrgID: uuid.MustParse("0196fca8-1c28-7b6e-987a-b08b165dbf30"),
		Slug:  "qa-245-o01-w01",
		Actors: []Actor{
			{UserID: deterministicUUID("actor-1"), Token: "token-1"},
			{UserID: deterministicUUID("actor-2"), Token: "token-2"},
			{UserID: deterministicUUID("actor-3"), Token: "token-3"},
		},
	}}}
	generator := NewGenerator(
		NewDriver(testGraph(handler), false, seed),
		content,
		identities,
		scale,
		seed,
		GeneratorOptions{},
	)
	summary, err := generator.Run(t.Context())
	if err != nil {
		t.Fatalf("Generator.Run() error = %v", err)
	}
	return summary
}

type rerunNode struct {
	name     string
	rawID    string
	rendered string
}

type rerunMCP struct {
	nodes                map[string]map[string]rerunNode
	invalidGetReferences []string
	newProbeCreates      int
}

func (f *rerunMCP) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var payload rpcRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	switch {
	case strings.HasPrefix(payload.Params.Name, "tack_create_"):
		f.create(writer, payload)
	case isCorpusListTool(payload.Params.Name):
		f.list(writer, payload.Params.Name)
	case payload.Params.Name == "tack_delete_issue":
		f.delete(payload.Params.Arguments.NodeID)
		writeRerunResult(writer, "ok", false)
	case strings.HasPrefix(payload.Params.Name, "tack_get_") &&
		payload.Params.Arguments.NodeID != "":
		f.get(writer, payload)
	default:
		writeRerunResult(writer, "ok", false)
	}
}

func (f *rerunMCP) create(writer http.ResponseWriter, payload rpcRequest) {
	toolName := payload.Params.Name
	args := payload.Params.Arguments
	key := strings.Join([]string{
		args.WorkspaceReference,
		args.ProjectReference,
		args.IssueReference,
		args.Name,
	}, "\x00")
	isProbe := strings.HasPrefix(args.Name, "QA deletion probe seed ")
	if f.nodes[toolName] == nil {
		f.nodes[toolName] = make(map[string]rerunNode)
	}
	stored, ok := f.nodes[toolName][key]
	if !ok {
		if isProbe {
			f.newProbeCreates++
		}
		rawID := deterministicUUID(toolName + ":" + key).String()
		rendered := rawID
		if toolName == "tack_create_label" {
			rendered = args.WorkspaceReference + "::" + args.Name
		}
		stored = rerunNode{
			name:     args.Name,
			rawID:    rawID,
			rendered: rendered,
		}
		f.nodes[toolName][key] = stored
	}
	writeRerunResult(writer, "- Raw id: `"+stored.rawID+"`", false)
}

func (f *rerunMCP) delete(rawID string) {
	for key, stored := range f.nodes["tack_create_issue"] {
		if stored.rawID == rawID {
			delete(f.nodes["tack_create_issue"], key)
			return
		}
	}
}

func (f *rerunMCP) list(writer http.ResponseWriter, toolName string) {
	createTool := corpusCreateTool(toolName)
	nodes := make([]rerunNode, 0, len(f.nodes[createTool]))
	for _, stored := range f.nodes[createTool] {
		nodes = append(nodes, stored)
	}
	sort.Slice(nodes, func(left, right int) bool {
		return nodes[left].name < nodes[right].name
	})
	var output strings.Builder
	fmt.Fprintf(&output, "%d %s found.\n", len(nodes), strings.TrimPrefix(toolName, "tack_list_"))
	for _, stored := range nodes {
		fmt.Fprintf(
			&output,
			"- `%s`\n  - Name: %s\n",
			stored.rendered,
			stored.name,
		)
	}
	writeRerunResult(writer, output.String(), false)
}

func (f *rerunMCP) get(writer http.ResponseWriter, payload rpcRequest) {
	if _, err := uuid.Parse(payload.Params.Arguments.NodeID); err != nil {
		f.invalidGetReferences = append(
			f.invalidGetReferences,
			payload.Params.Name+":"+payload.Params.Arguments.NodeID,
		)
		writeRerunResult(writer, "not found", true)
		return
	}
	writeRerunResult(writer, "ok", false)
}

func (f *rerunMCP) uniqueNodeCount() int {
	count := 0
	for _, nodes := range f.nodes {
		count += len(nodes)
	}
	return count
}
