package integration

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

const (
	maxSuccessTextBytes = 32 * 1024
	truncationNotice    = "\n\nOutput truncated at 32 KB. Narrow the request, lower `limit`, or use `cursor`."
	// hugeDescriptionSize exceeds the 32 KB cap while staying under
	// FoundationDB's 100,000-byte value limit, which refuses a 100 KB
	// description at create time (error 2103).
	hugeDescriptionSize = 60 * 1024
)

func TestDescribeWorkspaceCountsChildrenPerType(t *testing.T) {
	harness := NewMCPHarness(t)
	arguments := harness.projectArgs()
	arguments.ProjectReference = ""

	text := harness.Call(t, "tack_describe_workspace", arguments).Text()

	if strings.Contains(text, "#### Children") {
		t.Fatalf("describe still lists children:\n%s", text)
	}
	if !strings.Contains(text, "Direct children: 1") {
		t.Fatalf("describe lacks the project count:\n%s", text)
	}
}

func TestListCommentsPrintsNameOnce(t *testing.T) {
	harness := NewMCPHarness(t)
	issue := harness.CreateIssue(t, "commented issue")
	createArguments := harness.projectArgs()
	createArguments.IssueReference = issue
	createArguments.Name = "unique comment body"
	harness.Call(t, "tack_create_comment", createArguments)

	listArguments := harness.projectArgs()
	listArguments.IssueReference = issue
	text := harness.Call(t, "tack_list_comments", listArguments).Text()

	if strings.Count(text, "unique comment body") != 1 {
		t.Fatalf("comment name printed %d times:\n%s", strings.Count(text, "unique comment body"), text)
	}
}

func TestGetIssueCapsHugeDescription(t *testing.T) {
	harness := NewMCPHarness(t)
	createArguments := harness.projectArgs()
	createArguments.Name = "huge description"
	hugeDescription := strings.Repeat("Huge description line.\n", hugeDescriptionSize/len("Huge description line.\n"))
	createArguments.Properties = datagen.NodeProperties{"description": json.RawMessage(strconv.Quote(hugeDescription))}
	nodeID := harness.Call(t, "tack_create_issue", createArguments).RawID()
	if nodeID == "" {
		t.Fatal("create response has no raw id")
	}
	getArguments := harness.projectArgs()
	getArguments.ProjectReference = ""
	getArguments.NodeID = nodeID

	text := harness.Call(t, "tack_get_issue", getArguments).Text()

	if !strings.HasSuffix(text, truncationNotice) {
		t.Fatalf("get response does not end with the truncation notice; last bytes: %q", text[max(0, len(text)-200):])
	}
	if len(text) > maxSuccessTextBytes+len(truncationNotice) {
		t.Fatalf("get response is %d bytes, want at most %d", len(text), maxSuccessTextBytes+len(truncationNotice))
	}
}
