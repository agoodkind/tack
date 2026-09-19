package integration

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var nextCursorPattern = regexp.MustCompile("Next cursor: `([^`]+)`")

func TestListIssuesPagesThroughEveryIssue(t *testing.T) {
	harness := NewMCPHarness(t)
	for i := 0; i < pagedIssueCount; i++ {
		arguments := harness.projectArgs()
		arguments.Name = fmt.Sprintf("paged issue %03d", i)
		harness.Call(t, "tack_create_issue", arguments)
	}

	seen := map[string]bool{}
	arguments := harness.projectArgs()
	pageCount := 0
	for {
		text := harness.Call(t, "tack_list_issues", arguments).Text()
		pageCount++
		for i := 0; i < pagedIssueCount; i++ {
			name := fmt.Sprintf("paged issue %03d", i)
			if strings.Contains(text, name) {
				seen[name] = true
			}
		}
		match := nextCursorPattern.FindStringSubmatch(text)
		if match == nil {
			break
		}
		arguments.Cursor = match[1]
	}

	if len(seen) != pagedIssueCount {
		t.Fatalf("saw %d issues, want %d", len(seen), pagedIssueCount)
	}
	// 150 issues at the default 25 rows per page is six pages; one page
	// would mean the tool ignored the default limit.
	if pageCount != 6 {
		t.Fatalf("page count = %d, want 6", pageCount)
	}
}

func TestListIssuesRejectsLimitAboveMaximum(t *testing.T) {
	harness := NewMCPHarness(t)
	arguments := harness.projectArgs()
	arguments.Limit = 101

	text := harness.CallExpectError(t, "tack_list_issues", arguments)

	if !strings.Contains(text, "limit must be between 1 and 100") {
		t.Fatalf("unexpected error text:\n%s", text)
	}
}
