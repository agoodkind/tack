package integration

import "goodkind.io/tack/internal/datagen"

// projectArgs returns tool arguments addressed at the harness's workspace and
// project, with every other field empty for the caller to fill.
func (h *MCPHarness) projectArgs() datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: h.Workspace,
		ProjectReference:   h.Project,
		IssueReference:     "",
		Name:               "",
		Properties:         nil,
		NodeID:             "",
		Query:              "",
		NodeType:           "",
		Direction:          "",
		SourceID:           "",
		RelationType:       "",
		TargetID:           "",
		Limit:              0,
		Cursor:             "",
	}
}
