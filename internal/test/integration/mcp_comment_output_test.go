package integration

import (
	"fmt"
	"strings"
	"testing"
)

const (
	longCommentCount = 40
	longCommentBytes = 2048
	commentListLimit = 100
)

func longCommentBody(index int) string {
	prefix := fmt.Sprintf("long comment %03d ", index)
	return prefix + strings.Repeat("z", longCommentBytes-len(prefix))
}

// TestListCommentsReachesEveryLongCommentThroughCursor creates comments whose
// bodies together exceed the 32 KB response cap and lists them at limit 100.
// Every comment must be reached by following the cursor, and no page may be
// cut by the response cap.
func TestListCommentsReachesEveryLongCommentThroughCursor(t *testing.T) {
	harness := NewMCPHarness(t)
	issue := harness.CreateIssue(t, "long comments issue")
	for i := 0; i < longCommentCount; i++ {
		arguments := harness.projectArgs()
		arguments.IssueReference = issue
		arguments.Name = longCommentBody(i)
		harness.Call(t, "tack_create_comment", arguments)
	}

	seen := map[int]bool{}
	arguments := harness.projectArgs()
	arguments.IssueReference = issue
	arguments.Limit = commentListLimit
	pageCount := 0
	for {
		text := harness.Call(t, "tack_list_comments", arguments).Text()
		pageCount++
		if strings.Contains(text, "Output truncated") {
			t.Fatalf("page %d was cut by the response cap", pageCount)
		}
		for i := 0; i < longCommentCount; i++ {
			if strings.Contains(text, fmt.Sprintf("long comment %03d ", i)) {
				seen[i] = true
			}
		}
		match := nextCursorPattern.FindStringSubmatch(text)
		if match == nil {
			break
		}
		arguments.Cursor = match[1]
	}

	if len(seen) != longCommentCount {
		t.Fatalf("reached %d comments, want %d", len(seen), longCommentCount)
	}
	if pageCount < 2 {
		t.Fatalf("page count = %d, want the byte budget to split the list", pageCount)
	}
}
