package integration

import (
	"strings"
	"testing"
)

// TestCreateCommentReturnsShortConfirmation creates a 5 KB comment and checks
// that the confirmation stays under 1 KB instead of repeating the body.
func TestCreateCommentReturnsShortConfirmation(t *testing.T) {
	harness := NewMCPHarness(t)
	issue := harness.CreateIssue(t, "confirmation comment issue")
	arguments := harness.projectArgs()
	arguments.IssueReference = issue
	arguments.Name = "five kilobyte comment " + strings.Repeat("w", 5*1024)

	result := harness.Call(t, "tack_create_comment", arguments)

	text := result.Text()
	if len(text) > maxWriteConfirmationBytes {
		t.Fatalf("comment confirmation is %d bytes, want at most %d", len(text), maxWriteConfirmationBytes)
	}
	if result.RawID() == "" {
		t.Fatalf("comment confirmation has no raw id:\n%s", text)
	}
}
