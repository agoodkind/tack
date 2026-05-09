package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

const (
	addressBackfillStatusConflict   = "conflict"
	addressBackfillStatusIdempotent = "idempotent"
	addressBackfillStatusMalformed  = "malformed"
	addressBackfillStatusWrite      = "write"
	addressBackfillStatusWritten    = "written"
)

func buildAddressBackfillResult(
	ctx context.Context,
	operation string,
	mode string,
	controls addressBackfillControls,
	rows []fdbadapter.InspectLegacyAddressRow,
	truncated bool,
	inspector addressBackfillInspector,
	addresses addressBackfillAddressStore,
) (*addressBackfillResult, error) {
	result := &addressBackfillResult{
		Operation:       operation,
		Mode:            mode,
		Input:           controls,
		SourceCount:     0,
		CandidateCount:  0,
		WriteCount:      0,
		WrittenCount:    0,
		IdempotentCount: 0,
		ConflictCount:   0,
		MalformedCount:  0,
		SkippedCount:    0,
		Truncated:       truncated,
		Candidates:      make([]addressBackfillCandidate, 0, len(rows)),
		Warnings:        nil,
	}
	reports := make(map[uuid.UUID]*fdbadapter.NodeInspectionReport)
	for _, legacyRow := range rows {
		report, err := readAddressBackfillReport(ctx, inspector, reports, legacyRow.OwnerID)
		if err != nil {
			return nil, err
		}
		candidate, err := readAddressBackfillCandidate(ctx, legacyRow, report, addresses)
		if err != nil {
			return nil, err
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	sortAddressBackfillCandidates(result.Candidates)
	if len(result.Candidates) == 0 {
		result.Warnings = append(result.Warnings, "no legacy address rows found")
	}
	result.recount()
	return result, nil
}

func readAddressBackfillReport(
	ctx context.Context,
	inspector addressBackfillInspector,
	reports map[uuid.UUID]*fdbadapter.NodeInspectionReport,
	nodeID uuid.UUID,
) (*fdbadapter.NodeInspectionReport, error) {
	if report, ok := reports[nodeID]; ok {
		return report, nil
	}
	report, err := inspector.QueryNodeRecords(ctx, nodeID)
	if err != nil {
		return nil, errors.New("inspect legacy address owner " + nodeID.String() + ": " + err.Error())
	}
	reports[nodeID] = report
	return report, nil
}

func readAddressBackfillCandidate(
	ctx context.Context,
	legacyRow fdbadapter.InspectLegacyAddressRow,
	report *fdbadapter.NodeInspectionReport,
	addresses addressBackfillAddressStore,
) (addressBackfillCandidate, error) {
	candidate := addressBackfillCandidate{
		Source: legacyRow,
		Target: addressBackfillTarget{
			KeyFamily:    "address_index",
			OrgID:        uuid.Nil,
			NodeType:     legacyRow.NodeType,
			AddressKind:  string(node.AddressKindPrimary),
			AddressValue: legacyRow.AddressValue,
			NodeID:       legacyRow.OwnerID,
		},
		ExistingNodeID: nil,
		ResolvePresent: false,
		Status:         addressBackfillStatusWrite,
		Errors:         nil,
	}
	candidate.validate(report)
	if candidate.Status == addressBackfillStatusMalformed {
		return candidate, nil
	}
	existingNodeID, err := addresses.GetAddress(ctx, legacyRow.NodeType, node.AddressKindPrimary, legacyRow.AddressValue)
	if err != nil {
		message := "get target address " + legacyRow.NodeType + "/" + legacyRow.AddressValue + ": " + err.Error()
		return candidate, errors.New(message)
	}
	if existingNodeID == uuid.Nil {
		return candidate, nil
	}
	candidate.ExistingNodeID = &existingNodeID
	if existingNodeID == legacyRow.OwnerID {
		candidate.Status = addressBackfillStatusIdempotent
		return candidate, nil
	}
	candidate.Status = addressBackfillStatusConflict
	candidate.Errors = append(candidate.Errors, fmt.Sprintf("target address already points to %s", existingNodeID))
	return candidate, nil
}

func (candidate *addressBackfillCandidate) validate(report *fdbadapter.NodeInspectionReport) {
	if candidate.Source.OwnerID == uuid.Nil {
		candidate.markMalformed("legacy address row has no owner node id")
	}
	if candidate.Source.NodeType == "" {
		candidate.markMalformed("legacy address row has no node type")
	}
	if candidate.Source.AddressValue == "" {
		candidate.markMalformed("legacy address row has no address value")
	}
	if report == nil || report.Resolve == nil {
		candidate.markMalformed("owner node has no resolve row")
		return
	}
	candidate.ResolvePresent = true
	candidate.Target.OrgID = report.Resolve.OrgID
	if report.Resolve.NodeType == "" {
		candidate.markMalformed("owner resolve row has no node type")
	}
	if candidate.Source.NodeType != "" && report.Resolve.NodeType != "" && candidate.Source.NodeType != report.Resolve.NodeType {
		candidate.markMalformed("legacy node type does not match owner resolve node type")
	}
}

func (candidate *addressBackfillCandidate) markMalformed(message string) {
	candidate.Status = addressBackfillStatusMalformed
	candidate.Errors = append(candidate.Errors, message)
}

func sortAddressBackfillCandidates(candidates []addressBackfillCandidate) {
	sort.Slice(candidates, func(i int, j int) bool {
		return addressBackfillCandidateSortKey(candidates[i]) < addressBackfillCandidateSortKey(candidates[j])
	})
}

func sortLegacyAddressSourceRows(rows []fdbadapter.InspectLegacyAddressRow) {
	sort.Slice(rows, func(i int, j int) bool {
		return legacyAddressSourceSortKey(rows[i]) < legacyAddressSourceSortKey(rows[j])
	})
}

func addressBackfillCandidateSortKey(candidate addressBackfillCandidate) string {
	return legacyAddressSourceSortKey(candidate.Source)
}

func legacyAddressSourceSortKey(row fdbadapter.InspectLegacyAddressRow) string {
	return row.NodeType + "\x00" + row.AddressValue + "\x00" + row.OwnerID.String()
}
