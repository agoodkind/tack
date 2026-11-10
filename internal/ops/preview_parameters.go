package ops

import "goodkind.io/tack/internal/clispec"

func repairPreviewParams() []clispec.Param[repairPreviewInput] {
	return []clispec.Param[repairPreviewInput]{
		clispec.StringParam(
			"node",
			"node UUID",
			"",
			false,
			func(in *repairPreviewInput, v string) { in.NodeID = v },
		),
		clispec.StringParam(
			"class",
			"repair class",
			string(DefaultRepairClass()),
			false,
			func(in *repairPreviewInput, v string) { in.Class = v },
		),
		clispec.StringParam(
			"profile",
			"repair profile JSON file",
			"",
			false,
			func(in *repairPreviewInput, v string) { in.Profile = v },
		),
		clispec.StringParam(
			"manifest",
			"repair manifest JSON file",
			"",
			false,
			func(in *repairPreviewInput, v string) { in.Manifest = v },
		),
	}
}
