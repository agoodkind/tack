package ops

import (
	"context"
	"encoding/json"
	"fmt"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type datagenVerbReportCommand string

const (
	datagenLedgerVerbCommand   datagenVerbReportCommand = "ops.qa.datagen.ledger_verbs"
	datagenAccessVerbCommand   datagenVerbReportCommand = "ops.qa.datagen.search_access_verbs"
	datagenSettingsVerbCommand datagenVerbReportCommand = "ops.qa.datagen.search_settings_verbs"
	datagenSessionVerbCommand  datagenVerbReportCommand = "ops.qa.datagen.search_session_verbs"
)

// The audit fingerprint must match the printed report field byte for byte.
func renderDatagenReport[Report datagenReport](ctx context.Context, factory *cli.Factory, report Report) ([]byte, error) {
	result, field, err := report.reportResult()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	rendered, err := clispec.RenderJSON(ctx, factory.OutputFormat(), payload)
	if err != nil {
		return nil, err
	}
	if factory.OutputFormat() == cli.FormatJSON {
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(rendered, &envelope); err != nil {
			return nil, err
		}
		rendered = envelope.Result
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rendered, &fields); err != nil {
		return nil, err
	}
	value, found := fields[field]
	if !found {
		return nil, fmt.Errorf("the rendered result does not contain report field %s", field)
	}
	return value, nil
}

func (report datagenLedgerVerbsReport) reportResult() (clispec.Result, string, error) {
	var seed datagenSeedResult
	switch datagenVerbReportCommand(report.Command) {
	case datagenLedgerVerbCommand:
		seed.LedgerVerbs = &report
		return seed, "ledger_verbs", nil
	case datagenAccessVerbCommand:
		seed.SearchAccessVerbs = &report
		return seed, "search_access_verbs", nil
	case datagenSettingsVerbCommand:
		seed.SearchSettingsVerbs = &report
		return seed, "search_settings_verbs", nil
	case datagenSessionVerbCommand:
		var search datagenSearchResult
		search.SessionVerbs = &report
		return search, "session_verbs", nil
	default:
		return nil, "", fmt.Errorf("the verb report command %s does not have a result field", report.Command)
	}
}

func (report auditConsumerOffsetsReport) reportResult() (clispec.Result, string, error) {
	var seed datagenSeedResult
	seed.AuditConsumerOffsets = &report
	return seed, "audit_consumer_offsets", nil
}

func (report datagenCommitOrderReport) reportResult() (clispec.Result, string, error) {
	var seed datagenSeedResult
	seed.CommitOrder = &report
	return seed, "commit_order", nil
}

func (report datagenManifestVerificationReport) reportResult() (clispec.Result, string, error) {
	var seed datagenSeedResult
	seed.ManifestVerification = &report
	return seed, "manifest_verification", nil
}
