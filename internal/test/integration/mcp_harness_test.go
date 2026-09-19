package integration

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/datagen"
)

func TestMCPHarnessListsWorkspaces(t *testing.T) {
	harness := NewMCPHarness(t)

	result := harness.Call(t, "tack_list_workspaces", datagen.ToolArguments{})

	if !strings.Contains(result.Text(), harness.Workspace) {
		t.Fatalf("workspace %q missing from list:\n%s", harness.Workspace, result.Text())
	}
}
