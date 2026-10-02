package audit

const (
	// VerbOpsDBPlanOpen records an operator opening a break-glass plan: the
	// statements a later `ops db sql --plan-id` may run without a mail each.
	VerbOpsDBPlanOpen Verb = "ops.db_plan_open"
	// VerbOpsDBPlanClose records an operator closing a break-glass plan and
	// the summary mail of its statements. A close by another principal is
	// recorded with [OutcomeRefused] and leaves the plan open.
	VerbOpsDBPlanClose Verb = "ops.db_plan_close"
)
