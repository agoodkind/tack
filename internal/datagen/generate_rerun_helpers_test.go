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
