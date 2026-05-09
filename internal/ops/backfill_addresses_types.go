package ops

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

const (
	backfillAddressLimitEnv   = "TACK_BACKFILL_LIMIT"
	backfillAddressNodeIDEnv  = "TACK_BACKFILL_NODE_ID"
	backfillAddressApplyEnv   = "TACK_BACKFILL_APPLY"
	backfillAddressDefaultMax = 1000
)

type addressBackfillInspector interface {
	QueryNodeRecords(ctx context.Context, nodeID uuid.UUID) (*fdbadapter.NodeInspectionReport, error)
}

type addressBackfillAddressStore interface {
	GetAddress(ctx context.Context, nodeType string, addressKind node.AddressKind, address string) (uuid.UUID, error)
	WriteAddress(ctx context.Context, nodeType string, addressKind node.AddressKind, address string, nodeID uuid.UUID) error
}

type addressBackfillControls struct {
	Limit           int        `json:"limit"`
	NodeID          *uuid.UUID `json:"node_id,omitempty"`
	LegacyNodeIDEnv string     `json:"legacy_node_id_env,omitempty"`
	ApplyConfirmed  bool       `json:"apply_confirmed,omitempty"`
}

type addressBackfillResult struct {
	Operation       string                     `json:"operation"`
	Mode            string                     `json:"mode"`
	Input           addressBackfillControls    `json:"input"`
	SourceCount     int                        `json:"source_count"`
	CandidateCount  int                        `json:"candidate_count"`
	WriteCount      int                        `json:"write_count"`
	WrittenCount    int                        `json:"written_count"`
	IdempotentCount int                        `json:"idempotent_count"`
	ConflictCount   int                        `json:"conflict_count"`
	MalformedCount  int                        `json:"malformed_count"`
	SkippedCount    int                        `json:"skipped_count"`
	Truncated       bool                       `json:"truncated"`
	Candidates      []addressBackfillCandidate `json:"candidates"`
	Warnings        []string                   `json:"warnings,omitempty"`
}

type addressBackfillCandidate struct {
	Source         fdbadapter.InspectLegacyAddressRow `json:"source"`
	Target         addressBackfillTarget              `json:"target"`
	ExistingNodeID *uuid.UUID                         `json:"existing_node_id,omitempty"`
	ResolvePresent bool                               `json:"resolve_present"`
	Status         string                             `json:"status"`
	Errors         []string                           `json:"errors,omitempty"`
}

type addressBackfillTarget struct {
	KeyFamily    string    `json:"key_family"`
	OrgID        uuid.UUID `json:"org_id"`
	NodeType     string    `json:"node_type"`
	AddressKind  string    `json:"address_kind"`
	AddressValue string    `json:"address_value"`
	NodeID       uuid.UUID `json:"node_id"`
}

func readAddressBackfillControls() (addressBackfillControls, error) {
	controls := addressBackfillControls{
		Limit:           backfillAddressDefaultMax,
		NodeID:          nil,
		LegacyNodeIDEnv: "",
		ApplyConfirmed:  false,
	}
	if rawLimit, ok := os.LookupEnv(backfillAddressLimitEnv); ok && strings.TrimSpace(rawLimit) != "" {
		limit, err := strconv.Atoi(strings.TrimSpace(rawLimit))
		if err != nil || limit <= 0 {
			return controls, fmt.Errorf("%s must be a positive integer", backfillAddressLimitEnv)
		}
		controls.Limit = limit
	}
	if rawNodeID, ok := os.LookupEnv(backfillAddressNodeIDEnv); ok && strings.TrimSpace(rawNodeID) != "" {
		nodeID, err := uuid.Parse(strings.TrimSpace(rawNodeID))
		if err != nil {
			return controls, fmt.Errorf("%s must be a UUID, got %q", backfillAddressNodeIDEnv, rawNodeID)
		}
		controls.NodeID = &nodeID
		return controls, nil
	}
	if rawNodeID, ok := os.LookupEnv(repairNodeIDEnv); ok && strings.TrimSpace(rawNodeID) != "" {
		nodeID, err := uuid.Parse(strings.TrimSpace(rawNodeID))
		if err != nil {
			return controls, fmt.Errorf("%s must be a UUID, got %q", repairNodeIDEnv, rawNodeID)
		}
		controls.NodeID = &nodeID
		controls.LegacyNodeIDEnv = repairNodeIDEnv
	}
	return controls, nil
}

func requireAddressBackfillApply(controls *addressBackfillControls) error {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(backfillAddressApplyEnv)))
	if value != "true" {
		return fmt.Errorf("%s=true is required for backfill.addresses.apply", backfillAddressApplyEnv)
	}
	controls.ApplyConfirmed = true
	return nil
}
