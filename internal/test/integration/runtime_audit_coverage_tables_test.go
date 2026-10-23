package integration_test

// taskSevenPartTwo identifies the task and branch responsible for adding audit
// recording to pending commands.
const taskSevenPartTwo = "TACK-583 Task 7 part 2 (claude/tack-583-external-ops-2)"

// pendingCoverage maps commands awaiting audit recording to the task
// responsible for adding it. Replace a pending entry with a case when its
// command starts recording an audit class.
var pendingCoverage = map[string]string{
	"command ops backup yb-archive-node": taskSevenPartTwo,
	"command ops backup staleness-check": taskSevenPartTwo,
	"command ops search provision":       taskSevenPartTwo,
	"command ops search access-rollout":  taskSevenPartTwo,
}

// failureOnlyCoverage maps operations to dependencies that their successful
// paths require beyond the default test engines. TestSearchRuntimeAuditCoverage
// runs the OpenSearch cases successfully; the default suite checks their
// failure or dry run behavior.
var failureOnlyCoverage = map[string]string{
	"mcp tool tack_search":                   coverageNeedsOpenSearch,
	"command ops search verify":              coverageNeedsOpenSearch,
	"command ops qa datagen search":          coverageNeedsOpenSearch,
	"command ops qa datagen search-load":     coverageNeedsOpenSearch,
	"command ops qa datagen search-mixed":    coverageNeedsOpenSearch,
	"command ops backup fdb-continuous-init": "The successful path requires a FoundationDB cluster file at /etc/foundationdb on the Docker daemon host.",
	"command ops backup restore-drill":       "The successful path requires a FoundationDB continuous backup from ops backup fdb-continuous-init.",
	"command ops store exclude":              "The successful path requires a FoundationDB cluster with more than one storage process.",
}

// coverageNeedsOpenSearch marks operations that TestSearchRuntimeAuditCoverage
// must exercise with OpenSearch.
const coverageNeedsOpenSearch = "The successful path requires OpenSearch; the test runner starts it only when TACK_SEARCH_INTEGRATION=1."

// coverageExclusion records why an operation lacks an audit case and whether
// the test must verify static help output.
type coverageExclusion struct {
	reason   string
	helpOnly bool
}

const (
	// staticHelpReason explains why commands that only print static help do not
	// need audit cases.
	staticHelpReason = "prints static help text without contacting any service, reading system state, or changing anything"
	// firstTimeSetupReason explains why commands that create the audit
	// infrastructure lack audit cases.
	firstTimeSetupReason = "The command creates the audit infrastructure during first-time setup."
)

// helpGroupPaths lists group commands that must print static help without
// contacting services or reading system state.
var helpGroupPaths = []string{
	"ops", "ops act-as", "ops audit", "ops audit dlq", "ops auth", "ops backfill", "ops batch", "ops db",
	"ops deploy", "ops inspect", "ops ledger", "ops qa", "ops qa datagen", "ops repair", "ops search",
	"ops store", "ops validate", "ops verify",
}

// excludedCoverage returns operations that lack audit cases, with their reasons
// and static help check requirements.
func excludedCoverage() map[string]coverageExclusion {
	excluded := map[string]coverageExclusion{
		"command ops provision":              {reason: firstTimeSetupReason, helpOnly: false},
		"command ops ledger audit-bootstrap": {reason: firstTimeSetupReason, helpOnly: false},
		"command ops backup help":            {reason: staticHelpReason, helpOnly: true},
	}
	for _, path := range helpGroupPaths {
		excluded["command "+path] = coverageExclusion{reason: staticHelpReason, helpOnly: true}
	}
	return excluded
}
