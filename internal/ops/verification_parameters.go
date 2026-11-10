package ops

import "goodkind.io/tack/internal/clispec"

func verifyNodeParams() []clispec.Param[verifyNodeInput] {
	return []clispec.Param[verifyNodeInput]{
		clispec.StringParam(
			"node",
			"node UUID",
			"",
			true,
			func(in *verifyNodeInput, v string) { in.NodeID = v },
		),
		clispec.StringParam(
			"class",
			"optional repair class",
			"",
			false,
			func(in *verifyNodeInput, v string) { in.Class = v },
		),
		clispec.StringParam(
			"profile",
			"repair profile JSON file",
			"",
			false,
			func(in *verifyNodeInput, v string) { in.Profile = v },
		),
		clispec.BoolParam(
			"records",
			"include raw inspection rows",
			false,
			func(in *verifyNodeInput, v bool) { in.Records = v },
		),
	}
}
