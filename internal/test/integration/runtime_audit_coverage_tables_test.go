//go:build runtimeaudit

package integration_test

var pendingCoverage = map[string]string{}

// failureOnlyCoverage maps operations to dependencies that their successful
// paths require beyond the default test engines. TestSearchRuntimeAuditCoverage
// runs the OpenSearch cases successfully; the default suite checks their
// failure or dry run behavior.
var failureOnlyCoverage = map[string]string{
	"mcp tool tack_search":                coverageNeedsOpenSearch,
	"command ops search verify":           coverageNeedsOpenSearch,
	"command ops search settings":         coverageNeedsOpenSearch,
	"command ops qa datagen search":       coverageNeedsOpenSearch,
	"command ops qa datagen search-load":  coverageNeedsOpenSearch,
	"command ops qa datagen search-mixed": coverageNeedsOpenSearch,
	"command ops search provision":        "The successful path requires OpenSearch and a FoundationDB key prefix without a serving index; TestSearchRuntimeAuditProvisionCoverage runs it.",
}

const coverageNeedsOpenSearch = "The successful path requires OpenSearch; the test runner starts it only when TACK_SEARCH_INTEGRATION=1."

// coverageExclusion records why an operation lacks an audit case and whether
// the test must verify static help output.
type coverageExclusion struct {
	reason   string
	helpOnly bool
}

const (
	staticHelpReason     = "prints static help text without contacting any service, reading system state, or changing anything"
	firstTimeSetupReason = "The command creates the audit infrastructure during first-time setup."
)

var helpGroupPaths = []string{
	"ops", "ops act-as", "ops audit", "ops audit dlq", "ops auth", "ops backfill", "ops batch", "ops db",
	"ops deploy", "ops inspect", "ops ledger", "ops qa", "ops qa datagen", "ops repair", "ops search",
	"ops store", "ops validate", "ops verify",
}

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
