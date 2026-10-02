package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

// actualToolListPageLimit bounds one tools/list traversal against an actual server.
const actualToolListPageLimit = 10

// actualSearchEntryArgument reads tools/list for harness through server and
// returns the required tack_search entry argument from its input schema.
func actualSearchEntryArgument(t *testing.T, server *actualSearchServer, harness *MCPHarness, sessionID string) (string, error) {
	t.Helper()
	cursor := ""
	for range actualToolListPageLimit {
		parameters := map[string]any{}
		if cursor != "" {
			parameters["cursor"] = cursor
		}
		call, err := actualProcessRPC(t, server, harness, sessionID, "tools/list", parameters)
		if err != nil {
			return "", fmt.Errorf("actual process tools/list pid=%d cursor=%q: %w", server.command.Process.Pid, cursor, err)
		}
		var page struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Required []string `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(call.RawResult, &page); err != nil {
			return "", fmt.Errorf("decode actual process tools/list pid=%d request_id=%s: %w", server.command.Process.Pid, call.ID, err)
		}
		for _, tool := range page.Tools {
			if tool.Name == "tack_search" {
				return datagen.SearchEntryArgument(tool.InputSchema.Required)
			}
		}
		if page.NextCursor == "" {
			return "", fmt.Errorf("actual process tools/list pid=%d returned no tack_search tool", server.command.Process.Pid)
		}
		cursor = page.NextCursor
	}
	return "", fmt.Errorf("actual process tools/list pid=%d did not finish within %d pages", server.command.Process.Pid, actualToolListPageLimit)
}

// actualSearchRefusal reports whether err from a tack_search call under entry
// is an authorization refusal by the rule in datagen.IsSearchAuthorizationRefusal.
func actualSearchRefusal(err error, entry string) bool {
	var statusError actualHTTPStatusError
	if errors.As(err, &statusError) {
		return datagen.IsSearchAuthorizationRefusal(statusError.StatusCode, "", entry)
	}
	var toolError actualToolError
	if errors.As(err, &toolError) {
		return datagen.IsSearchAuthorizationRefusal(0, toolError.Text, entry)
	}
	return false
}
