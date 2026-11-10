package ops

import "goodkind.io/tack/internal/clispec"

func validateNodeParams() []clispec.Param[validateNodeInput] {
	return []clispec.Param[validateNodeInput]{
		clispec.StringParam(
			"node",
			"node UUID",
			"",
			true,
			func(in *validateNodeInput, v string) { in.NodeID = v },
		),
		clispec.StringParam(
			"class",
			"repair class",
			string(DefaultRepairClass()),
			false,
			func(in *validateNodeInput, v string) { in.Class = v },
		),
		clispec.StringParam(
			"profile",
			"repair profile JSON file",
			"",
			false,
			func(in *validateNodeInput, v string) { in.Profile = v },
		),
	}
}
