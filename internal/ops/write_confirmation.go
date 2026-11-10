package ops

import "goodkind.io/tack/internal/clispec"

func repairApplyParams() []clispec.Param[repairApplyInput] {
	return []clispec.Param[repairApplyInput]{
		clispec.StringParam(
			"node",
			"node UUID",
			"",
			false,
			func(in *repairApplyInput, v string) { in.NodeID = v },
		),
		clispec.StringParam(
			"actor",
			"actor UUID",
			"",
			true,
			func(in *repairApplyInput, v string) { in.ActorID = v },
		),
		clispec.StringParam(
			"class",
			"repair class",
			string(DefaultRepairClass()),
			false,
			func(in *repairApplyInput, v string) { in.Class = v },
		),
		clispec.StringParam(
			"profile",
			"repair profile JSON file",
			"",
			false,
			func(in *repairApplyInput, v string) { in.Profile = v },
		),
		clispec.StringParam(
			"manifest",
			"repair manifest JSON file",
			"",
			false,
			func(in *repairApplyInput, v string) { in.Manifest = v },
		),
		clispec.StringParam(
			"confirm",
			"preview confirmation token",
			"",
			false,
			func(in *repairApplyInput, v string) { in.Confirm = v },
		),
		clispec.BoolParam(
			"yes",
			"confirm that this command may write",
			false,
			func(in *repairApplyInput, v bool) { in.Yes = v },
		),
	}
}

func newRepairApplyInput() repairApplyInput {
	return repairApplyInput{
		InputMarker: clispec.InputMarker{},
		NodeID:      "",
		ActorID:     "",
		Class:       string(DefaultRepairClass()),
		Profile:     "",
		Manifest:    "",
		Confirm:     "",
		Yes:         false,
	}
}
