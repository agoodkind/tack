package integration

import "testing"

type outageNodeSpec struct {
	singular string
	plural   string
}

func nonSearchOutageScenarios() []outageScenario {
	nodeSpecs := []outageNodeSpec{
		{singular: "project", plural: "projects"},
		{singular: "label", plural: "labels"},
		{singular: "state", plural: "states"},
		{singular: "epic", plural: "epics"},
		{singular: "cycle", plural: "cycles"},
		{singular: "module", plural: "modules"},
		{singular: "issue", plural: "issues"},
		{singular: "comment", plural: "comments"},
		{singular: "activity", plural: "activities"},
	}
	scenarios := []outageScenario{
		{name: "tack_list_workspaces", run: func(t *testing.T, harness *MCPHarness, _ *outageScenarioState) {
			callToolSuccess(t, harness, "tack_list_workspaces", map[string]any{})
		}},
		{name: "tack_describe_workspace", run: func(t *testing.T, harness *MCPHarness, _ *outageScenarioState) {
			callToolSuccess(t, harness, "tack_describe_workspace", workspaceArgs(harness))
		}},
		{name: "tack_list_members", run: func(t *testing.T, harness *MCPHarness, _ *outageScenarioState) {
			callToolSuccess(t, harness, "tack_list_members", workspaceArgs(harness))
		}},
		{name: "tack_list_property_defs", run: func(t *testing.T, harness *MCPHarness, _ *outageScenarioState) {
			callToolSuccess(t, harness, "tack_list_property_defs", workspaceArgs(harness))
		}},
	}
	for _, spec := range nodeSpecs {
		scenarios = append(scenarios, createOutageScenario(spec))
	}
	for _, spec := range nodeSpecs {
		scenarios = append(scenarios, listOutageScenario(spec))
	}
	for _, spec := range nodeSpecs {
		scenarios = append(scenarios, getOutageScenario(spec))
	}
	for _, spec := range nodeSpecs {
		scenarios = append(scenarios, updateOutageScenario(spec))
	}
	scenarios = append(scenarios, specialOutageScenarios()...)
	for i := len(nodeSpecs) - 1; i >= 0; i-- {
		scenarios = append(scenarios, deleteOutageScenario(nodeSpecs[i]))
	}
	return scenarios
}

func createOutageScenario(spec outageNodeSpec) outageScenario {
	return outageScenario{
		name: "tack_create_" + spec.singular,
		run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			text := callToolSuccess(t, harness, "tack_create_"+spec.singular, createArgs(harness, state, spec.singular))
			state.ids[spec.singular] = rawNodeID(t, text)
		},
	}
}

func listOutageScenario(spec outageNodeSpec) outageScenario {
	return outageScenario{
		name: "tack_list_" + spec.plural,
		run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_list_"+spec.plural, listArgs(harness, state, spec.singular))
		},
	}
}

func getOutageScenario(spec outageNodeSpec) outageScenario {
	return outageScenario{
		name: "tack_get_" + spec.singular,
		run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_get_"+spec.singular, nodeIDArgs(harness, state.ids[spec.singular]))
		},
	}
}

func updateOutageScenario(spec outageNodeSpec) outageScenario {
	return outageScenario{
		name: "tack_update_" + spec.singular,
		run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			args := nodeIDArgs(harness, state.ids[spec.singular])
			args["name"] = "Updated outage " + spec.singular
			callToolSuccess(t, harness, "tack_update_"+spec.singular, args)
		},
	}
}

func deleteOutageScenario(spec outageNodeSpec) outageScenario {
	return outageScenario{
		name: "tack_delete_" + spec.singular,
		run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_delete_"+spec.singular, nodeIDArgs(harness, state.ids[spec.singular]))
		},
	}
}

func specialOutageScenarios() []outageScenario {
	return []outageScenario{
		{name: "tack_set_issue_state", run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_set_issue_state", stateSetterArgs(harness, state, "issue"))
		}},
		{name: "tack_set_epic_state", run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_set_epic_state", stateSetterArgs(harness, state, "epic"))
		}},
		{name: "tack_get_properties", run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_get_properties", map[string]any{"node_id": state.ids["issue"]})
		}},
		{name: "tack_add_relationship", run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_add_relationship", relationshipArgs(state))
		}},
		{name: "tack_list_relationships", run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_list_relationships", map[string]any{
				"node_id": state.ids["issue"], "direction": "out", "relation_type": "outage_acceptance",
			})
		}},
		{name: "tack_remove_relationship", run: func(t *testing.T, harness *MCPHarness, state *outageScenarioState) {
			callToolSuccess(t, harness, "tack_remove_relationship", relationshipArgs(state))
		}},
	}
}
