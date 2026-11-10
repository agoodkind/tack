package ops

import "goodkind.io/tack/internal/clispec"

const inspectQueryDefaultLimit = 50

func inspectQueryParams() []clispec.Param[inspectQueryInput] {
	return []clispec.Param[inspectQueryInput]{
		clispec.StringParam(
			"org",
			"org UUID",
			"",
			true,
			func(in *inspectQueryInput, v string) { in.OrgID = v },
		),
		clispec.StringParam(
			"type",
			"node type key",
			"",
			false,
			func(in *inspectQueryInput, v string) { in.NodeType = v },
		),
		clispec.StringParam(
			"property",
			"property name",
			"",
			true,
			func(in *inspectQueryInput, v string) { in.Property = v },
		),
		clispec.StringParam(
			"value",
			"property value as JSON",
			"",
			true,
			func(in *inspectQueryInput, v string) { in.Value = v },
		),
		clispec.IntParam(
			"limit",
			"max rows",
			inspectQueryDefaultLimit,
			func(in *inspectQueryInput, v int) { in.Limit = v },
		),
	}
}
