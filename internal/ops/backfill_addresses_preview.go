package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
)

func init() {
	Register(Operation{
		Name:        "backfill.addresses.preview",
		Description: "Preview generic address index migration candidates without writing state.",
		Run:         runAddressMigrationPreview,
	})
}

type addressMigrationPreviewResult struct {
	Operation  string                      `json:"operation"`
	Mode       string                      `json:"mode"`
	NodeID     uuid.UUID                   `json:"node_id"`
	Count      int                         `json:"count"`
	Candidates []addressMigrationCandidate `json:"candidates"`
	Warnings   []string                    `json:"warnings,omitempty"`
}

type addressMigrationCandidate struct {
	Source      fdbadapter.InspectLegacyAddressRow `json:"source"`
	Target      addressMigrationTarget             `json:"target"`
	NodePresent bool                               `json:"node_present"`
	Warnings    []string                           `json:"warnings,omitempty"`
}

type addressMigrationTarget struct {
	KeyFamily    string    `json:"key_family"`
	OrgID        uuid.UUID `json:"org_id"`
	ScopeID      uuid.UUID `json:"scope_id"`
	NodeType     string    `json:"node_type"`
	AddressKind  string    `json:"address_kind"`
	AddressValue string    `json:"address_value"`
	NodeID       uuid.UUID `json:"node_id"`
}

func runAddressMigrationPreview(ctx context.Context, env *Env) error {
	nodeID, err := readRequiredUUIDEnv(repairNodeIDEnv)
	if err != nil {
		return err
	}
	report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
	if err != nil {
		env.Log.ErrorContext(ctx, "backfill.addresses.preview: inspect node",
			slog.String("node_id", nodeID.String()),
			slog.String("err", err.Error()))
		return fmt.Errorf("inspect node %s for address migration preview: %w", nodeID, err)
	}
	result := addressMigrationPreviewResult{
		Operation:  "backfill.addresses.preview",
		Mode:       "preview",
		NodeID:     nodeID,
		Count:      0,
		Candidates: make([]addressMigrationCandidate, 0, len(report.LegacyAddressRows)),
		Warnings:   nil,
	}
	for _, legacyRow := range report.LegacyAddressRows {
		candidate := buildAddressMigrationCandidate(report, legacyRow)
		result.Candidates = append(result.Candidates, candidate)
	}
	sort.Slice(result.Candidates, func(i int, j int) bool {
		return result.Candidates[i].Target.AddressValue < result.Candidates[j].Target.AddressValue
	})
	result.Count = len(result.Candidates)
	if result.Count == 0 {
		result.Warnings = append(result.Warnings, "no legacy address rows found for node")
	}
	return writeRepairOutput(result)
}

func buildAddressMigrationCandidate(
	report *fdbadapter.NodeInspectionReport,
	legacyRow fdbadapter.InspectLegacyAddressRow,
) addressMigrationCandidate {
	orgID := uuid.Nil
	if report.Resolve != nil {
		orgID = report.Resolve.OrgID
	}
	warnings := make([]string, 0)
	if report.Resolve == nil {
		warnings = append(warnings, "target node has no resolve row")
	}
	return addressMigrationCandidate{
		Source: legacyRow,
		Target: addressMigrationTarget{
			KeyFamily:    "node_address",
			OrgID:        orgID,
			ScopeID:      uuid.Nil,
			NodeType:     legacyRow.NodeType,
			AddressKind:  legacyRow.AddressKind,
			AddressValue: legacyRow.AddressValue,
			NodeID:       legacyRow.OwnerID,
		},
		NodePresent: report.Resolve != nil,
		Warnings:    warnings,
	}
}
