package datagen

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestDiscoverSoakProjectsRecoversCycleRenderedByName(t *testing.T) {
	const seed int64 = 777
	fake := &cycleNameDiscoveryMCP{}
	content, err := NewContent(t.Context(), seed)
	if err != nil {
		t.Fatalf("NewContent() error = %v", err)
	}
	scale, err := ParseScale("small")
	if err != nil {
		t.Fatalf("ParseScale() error = %v", err)
	}
	session := &runSession{
		driver:  NewDriver(testGraph(fake), false, seed),
		content: content,
		identities: Identities{Workspaces: []WorkspaceIdentity{{
			Slug: "qa-777-o01-w01",
			Actors: []Actor{{
				Token: "token",
			}},
		}}},
		scale: scale,
		seed:  seed,
	}

	projects, complete, err := discoverSoakProjects(t.Context(), session)
	if err != nil {
		t.Fatalf("discoverSoakProjects() error = %v", err)
	}
	if !complete || len(projects) != 1 || len(projects[0].Cycles) != 1 {
		t.Fatalf("discoverSoakProjects() = %d projects, complete %t", len(projects), complete)
	}
	assertRawUUID(t, "cycle", projects[0].Cycles[0].RawID)
	if len(fake.cycleNameGets) != 0 {
		t.Fatalf("cycle name gets = %v, want none", fake.cycleNameGets)
	}
	if fake.cycleCreateCount != 1 {
		t.Fatalf("cycle create count = %d, want 1", fake.cycleCreateCount)
	}
}

type cycleNameDiscoveryMCP struct {
	cycleNameGets    []string
	cycleCreateCount int
}

func (f *cycleNameDiscoveryMCP) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var payload rpcRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	args := payload.Params.Arguments
	switch payload.Params.Name {
	case "tack_list_projects":
		writeDiscoveryList(writer, "projects", "Q0101", "QA Project 1.1 seed 777")
	case "tack_list_labels":
		writeDiscoveryList(writer, "labels", deterministicUUID("label").String(), "label")
	case "tack_list_states":
		writeDiscoveryList(writer, "states", "Q0101::Todo", "Todo")
	case "tack_list_issues":
		writeDiscoveryList(writer, "issues", "Q0101-1", "Existing issue")
	case "tack_list_epics":
		writeDiscoveryList(writer, "epics", "Q0101-2", "Existing epic")
	case "tack_list_cycles":
		writeDiscoveryList(writer, "cycles", "QA cycle 1 seed 777", "QA cycle 1 seed 777")
	case "tack_list_modules":
		writeDiscoveryList(writer, "modules", "Q0101-4", "Existing module")
	case "tack_get_cycle":
		if _, err := uuid.Parse(args.NodeID); err != nil {
			f.cycleNameGets = append(f.cycleNameGets, args.NodeID)
			writeRerunResult(
				writer,
				`invalid node_id "QA cycle 1 seed 777": must be a UUID or sequence reference like TACK-65`,
				true,
			)
			return
		}
		writeDiscoveryNode(writer, deterministicUUID("cycle").String(), "", 0)
	case "tack_create_cycle":
		f.cycleCreateCount++
		projectID := deterministicUUID("tack_get_project").String()
		if args.Name != "QA cycle 1 seed 777" || args.ProjectReference != projectID {
			http.Error(writer, "unexpected cycle recovery arguments", http.StatusBadRequest)
			return
		}
		if len(args.Properties) != 2 {
			http.Error(writer, "incomplete cycle recovery properties", http.StatusBadRequest)
			return
		}
		writeDiscoveryNode(writer, deterministicUUID("cycle").String(), "", 0)
	case "tack_get_project", "tack_get_state", "tack_get_issue", "tack_get_epic", "tack_get_module":
		writeDiscoveryNode(writer, deterministicUUID(payload.Params.Name).String(), "unstarted", 10)
	default:
		http.Error(writer, "unexpected tool "+payload.Params.Name, http.StatusBadRequest)
	}
}
