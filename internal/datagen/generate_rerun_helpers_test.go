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
