package datagen

import (
	"encoding/json"
	"net/http"
)

func writeRerunResult(writer http.ResponseWriter, text string, isError bool) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(rpcResponse{
		JSONRPC: jsonRPCVersion,
		ID:      "1",
		Result: Result{
			Content: []ToolContent{{Type: "text", Text: text}},
			IsError: isError,
		},
	})
}

// deleteProject removes the project with rawID and every node created with
// the project's identifier as its project reference, the way the server's
// cascading delete removes a project's descendants. It returns the count of
// removed nodes.
func (f *rerunMCP) deleteProject(rawID string) int {
	identifier := ""
	for key, stored := range f.nodes["tack_create_project"] {
		if stored.rawID == rawID {
			identifier = stored.identifier
			delete(f.nodes["tack_create_project"], key)
		}
	}
	if identifier == "" {
		return 0
	}
	if f.deletedRawIDs == nil {
		f.deletedRawIDs = map[string]bool{}
	}
	f.deletedRawIDs[rawID] = true
	deleted := 1
	for _, nodes := range f.nodes {
		for key, stored := range nodes {
			if stored.project == identifier {
				f.deletedRawIDs[stored.rawID] = true
				delete(nodes, key)
				deleted++
			}
		}
	}
	return deleted
}

func isCorpusListTool(toolName string) bool {
	return corpusCreateTool(toolName) != ""
}

func corpusCreateTool(listTool string) string {
	return map[string]string{
		"tack_list_projects":   "tack_create_project",
		"tack_list_labels":     "tack_create_label",
		"tack_list_states":     "tack_create_state",
		"tack_list_epics":      "tack_create_epic",
		"tack_list_cycles":     "tack_create_cycle",
		"tack_list_modules":    "tack_create_module",
		"tack_list_issues":     "tack_create_issue",
		"tack_list_comments":   "tack_create_comment",
		"tack_list_activities": "tack_create_activity",
	}[listTool]
}
