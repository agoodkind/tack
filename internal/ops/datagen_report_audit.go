package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type datagenReport interface {
	datagenLedgerVerbsReport | datagenCommitOrderReport | auditConsumerOffsetsReport | datagenManifestVerificationReport
}

// datagenReportSelection stores safe inputs for report selection.
type datagenReportSelection struct {
	Report      string      `json:"report"`
	OrgIDs      []uuid.UUID `json:"org_ids,omitempty" exhaustruct:"optional"`
	Verbs       []string    `json:"verbs,omitempty" exhaustruct:"optional"`
	Topic       string      `json:"topic,omitempty" exhaustruct:"optional"`
	RequestID   string      `json:"request_id,omitempty" exhaustruct:"optional"`
	Oldest      time.Time   `json:"oldest,omitzero" exhaustruct:"optional"`
	Latest      time.Time   `json:"latest,omitzero" exhaustruct:"optional"`
	LimitPerOrg int         `json:"limit_per_org,omitempty" exhaustruct:"optional"`
	EntityKind  string      `json:"aggregate_entity_kind,omitempty" exhaustruct:"optional"`
	Wait        string      `json:"wait,omitempty" exhaustruct:"optional"`
	Poll        string      `json:"poll,omitempty" exhaustruct:"optional"`
}

// recordDatagenReport admits the complete report before its first query and
// returns its value only after finalization records its fingerprint and
// result identities.
func recordDatagenReport[Report datagenReport](
	ctx context.Context,
	factory *cli.Factory,
	spec audit.Spec,
	selection datagenReportSelection,
	read func(context.Context) (Report, []audit.Target, error),
) (Report, error) {
	var report, empty Report
	input, err := json.Marshal(selection)
	if err != nil {
		return empty, datagenReportFailure(ctx, selection.Report, err)
	}
	_, err = clispec.RunAuditedRead(ctx, factory, spec, input, func(ctx context.Context) (audit.OperatorReadOutput, error) {
		value, targets, err := read(ctx)
		if err != nil {
			return audit.OperatorReadOutput{}, err
		}
		payload, err := json.Marshal(value)
		if err != nil {
			return audit.OperatorReadOutput{}, datagenReportFailure(ctx, selection.Report, err)
		}
		report = value
		return audit.OperatorReadOutput{Payload: payload, Format: "application/json", Targets: targets}, nil
	})
	if err != nil {
		return empty, datagenReportFailure(ctx, selection.Report, err)
	}
	return report, nil
}

func datagenReportFailure(ctx context.Context, report string, err error) error {
	wrapped := fmt.Errorf("record generated data report %s: %w", report, err)
	slog.ErrorContext(ctx, "qa.datagen.report_failed", slog.String("report", report), slog.String("err", wrapped.Error()))
	return wrapped
}

func datagenVerbReportSpec(orgID uuid.UUID) audit.Spec {
	target := audit.ResourceTarget(audit.AuditOrgURIPrefix + orgID.String())
	return audit.Spec{
		Verb: string(audit.VerbAuditRead), Reads: true, Class: audit.OperationRead,
		Targets:   func(context.Context, json.RawMessage) ([]audit.Target, error) { return []audit.Target{target}, nil },
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
}

func recordDatagenVerbReport(
	ctx context.Context, factory *cli.Factory, orgID uuid.UUID, command string, verbs []string,
	wait bool, read func(context.Context) (datagenLedgerVerbsReport, error),
) (datagenLedgerVerbsReport, error) {
	selection := datagenReportSelection{Report: command, OrgIDs: []uuid.UUID{orgID}, Verbs: verbs}
	if wait {
		selection.Wait, selection.Poll = datagenSessionRowsWait.String(), datagenSessionRowsPoll.String()
	}
	return recordDatagenReport(ctx, factory, datagenVerbReportSpec(orgID), selection,
		func(ctx context.Context) (datagenLedgerVerbsReport, []audit.Target, error) {
			report, err := read(ctx)
			targets := make([]audit.Target, 0, len(report.Verbs))
			for _, entry := range report.Verbs {
				uri := audit.AuditOrgURIPrefix + orgID.String() + "/verb/" + url.PathEscape(entry.Verb)
				targets = append(targets, audit.ResourceTarget(uri))
			}
			return report, targets, err
		})
}
