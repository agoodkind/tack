package integration

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

const maxWriteConfirmationBytes = 1024

func longDescriptionProperties() datagen.NodeProperties {
	longDescription := strings.Repeat("A long description line.\n", 400)
	return datagen.NodeProperties{"description": json.RawMessage(strconv.Quote(longDescription))}
}

func assertShortConfirmation(t *testing.T, text string, name string) {
	t.Helper()
	if len(text) > maxWriteConfirmationBytes {
		t.Fatalf("write response is %d bytes, want at most %d", len(text), maxWriteConfirmationBytes)
	}
	if !strings.Contains(text, name) || !strings.Contains(text, "description") {
		t.Fatalf("confirmation lacks name or changed field:\n%s", text)
	}
}

func TestCreateIssueReturnsShortConfirmation(t *testing.T) {
	harness := NewMCPHarness(t)
	arguments := harness.projectArgs()
	arguments.Name = "confirmation check"
	arguments.Properties = longDescriptionProperties()

	text := harness.Call(t, "tack_create_issue", arguments).Text()

	assertShortConfirmation(t, text, "confirmation check")
}

func TestUpdateIssueReturnsShortConfirmation(t *testing.T) {
	harness := NewMCPHarness(t)
	createArguments := harness.projectArgs()
	createArguments.Name = "update confirmation check"
	created := harness.Call(t, "tack_create_issue", createArguments)
	nodeID := created.RawID()
	if nodeID == "" {
		t.Fatalf("create response has no raw id:\n%s", created.Text())
	}
	updateArguments := harness.projectArgs()
	updateArguments.ProjectReference = ""
	updateArguments.NodeID = nodeID
	updateArguments.Properties = longDescriptionProperties()

	text := harness.Call(t, "tack_update_issue", updateArguments).Text()

	assertShortConfirmation(t, text, "update confirmation check")
}
