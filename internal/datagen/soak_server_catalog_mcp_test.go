package datagen

import (
	"encoding/json"
	"net/http"
	"strings"
)

type serverCatalogMCP struct {
	projects          map[string][]collectionItem
	failedLists       map[string]bool
	projectReferences []string
}

func (f *serverCatalogMCP) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	var payload rpcRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	toolName := payload.Params.Name
	arguments := payload.Params.Arguments
	if strings.HasPrefix(toolName, "tack_list_") {
		f.list(writer, toolName, arguments)
		return
	}
	if strings.HasPrefix(toolName, "tack_create_") {
		rawID := deterministicUUID(toolName + ":" + arguments.Name).String()
		writeDiscoveryNode(writer, rawID, "started", 20)
		return
	}
	if strings.HasPrefix(toolName, "tack_get_") {
		writeDiscoveryNode(writer, deterministicUUID(arguments.NodeID).String(), "unstarted", 10)
		return
	}
	writeRerunResult(writer, "ok", false)
}

func (f *serverCatalogMCP) list(
	writer http.ResponseWriter,
	toolName string,
	arguments ToolArguments,
) {
	if toolName == "tack_list_projects" {
		writeDiscoveryListItems(writer, "projects", f.projects[arguments.WorkspaceReference])
		return
	}
	if arguments.ProjectReference != "" {
		f.projectReferences = append(f.projectReferences, arguments.ProjectReference)
	}
	if f.failedLists[toolName+":"+arguments.ProjectReference] {
		writeRerunResult(writer, "injected list failure", true)
		return
	}
	switch toolName {
	case "tack_list_states":
		writeDiscoveryList(
			writer,
			"states",
			deterministicUUID(arguments.ProjectReference+":state").String(),
			"Todo",
		)
	case "tack_list_issues":
		writeDiscoveryList(
			writer,
			"issues",
			deterministicUUID(arguments.ProjectReference+":issue").String(),
			"Existing issue",
		)
	default:
		plural := strings.TrimPrefix(toolName, "tack_list_")
		writeDiscoveryListItems(writer, plural, nil)
	}
}
