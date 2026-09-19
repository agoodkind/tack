package datagen

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// answerOther serves tack_search over the stored issues and answers every
// other unmodeled tool with a plain success.
func (f *rerunMCP) answerOther(writer http.ResponseWriter, payload rpcRequest) {
	if payload.Params.Name != "tack_search" {
		writeRerunResult(writer, "ok", false)
		return
	}
	query := strings.ToLower(payload.Params.Arguments.Query)
	matches := make([]rerunNode, 0)
	for _, stored := range f.nodes["tack_create_issue"] {
		if strings.Contains(strings.ToLower(stored.name), query) {
			matches = append(matches, stored)
		}
	}
	sort.Slice(matches, func(left, right int) bool {
		return matches[left].name < matches[right].name
	})
	var output strings.Builder
	fmt.Fprintf(&output, "%d results shown.\n", len(matches))
	for _, stored := range matches {
		fmt.Fprintf(&output, "- `%s`\n  - Name: %s\n  - Type: `issue`\n", stored.rendered, stored.name)
	}
	writeRerunResult(writer, output.String(), false)
}
