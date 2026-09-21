package integration

const (
	outageProjectReference      = "OUTAGE"
	updatedOutageStateReference = outageProjectReference + "::Updated outage state"
)

func createArgs(harness *MCPHarness, state *outageScenarioState, nodeType string) map[string]any {
	args := workspaceArgs(harness)
	args["name"] = "Outage " + nodeType
	switch nodeType {
	case "project":
		args["properties"] = map[string]any{"identifier": outageProjectReference}
	case "state", "epic", "cycle", "module", "issue":
		args["project_reference"] = state.ids["project"]
	case "comment", "activity":
		args["project_reference"] = state.ids["project"]
		args["issue_reference"] = state.ids["issue"]
	}
	return args
}

func listArgs(harness *MCPHarness, state *outageScenarioState, nodeType string) map[string]any {
	args := workspaceArgs(harness)
	switch nodeType {
	case "state", "epic", "cycle", "module", "issue":
		args["project_reference"] = state.ids["project"]
	case "comment", "activity":
		args["project_reference"] = state.ids["project"]
		args["issue_reference"] = state.ids["issue"]
	}
	return args
}

func stateSetterArgs(harness *MCPHarness, state *outageScenarioState, nodeType string) map[string]any {
	return map[string]any{
		"workspace_reference":   harness.Workspace,
		"project_reference":     state.ids["project"],
		nodeType + "_reference": state.ids[nodeType],
		"state":                 updatedOutageStateReference,
	}
}

func relationshipArgs(state *outageScenarioState) map[string]any {
	return map[string]any{
		"source_id":     state.ids["issue"],
		"relation_type": "outage_acceptance",
		"target_id":     state.ids["label"],
	}
}

func workspaceArgs(harness *MCPHarness) map[string]any {
	return map[string]any{"workspace_reference": harness.Workspace}
}

func nodeIDArgs(harness *MCPHarness, nodeID string) map[string]any {
	return map[string]any{"workspace_reference": harness.Workspace, "node_id": nodeID}
}
