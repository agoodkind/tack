package ops

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

const (
	repairNodeIDEnv       = "TACK_REPAIR_NODE_ID"
	repairFindModeEnv     = "TACK_REPAIR_FIND_MODE"
	repairFindNodeTypeEnv = "TACK_REPAIR_FIND_NODE_TYPE"
	repairFindSlugEnv     = "TACK_REPAIR_FIND_SLUG"
	repairFindPropEnv     = "TACK_REPAIR_FIND_PROPERTY"
	repairFindValueEnv    = "TACK_REPAIR_FIND_VALUE_JSON"
	repairFindOrgIDEnv    = "TACK_REPAIR_FIND_ORG_ID"
)

type repairSelectedNode struct {
	NodeID             uuid.UUID         `json:"node_id"`
	Resolve            *node.NodeResolve `json:"resolve,omitempty"`
	Node               *node.Node        `json:"node,omitempty"`
	NodeView           *node.NodeView    `json:"node_view,omitempty"`
	NodeInstanceCount  int               `json:"node_instance_count"`
	NodeViewCount      int               `json:"node_view_count"`
	PropertyIndexCount int               `json:"property_index_count"`
	SlugRowCount       int               `json:"slug_row_count"`
	OutgoingRelCount   int               `json:"outgoing_relationship_count"`
	IncomingRelCount   int               `json:"incoming_relationship_count"`
	Warnings           []string          `json:"warnings,omitempty"`
}

type repairFindResult struct {
	Mode        string                            `json:"mode"`
	Input       map[string]string                 `json:"input"`
	Matches     []repairSelectedNode              `json:"matches,omitempty"`
	Inspections []fdbadapter.NodeInspectionReport `json:"inspections,omitempty"`
	Warnings    []string                          `json:"warnings,omitempty"`
}

type repairQueryResult struct {
	NodeID                uuid.UUID                        `json:"node_id"`
	Selection             *repairSelectedNode              `json:"selection,omitempty"`
	Raw                   *fdbadapter.NodeInspectionReport `json:"raw,omitempty"`
	IndexedPropertyChecks []repairIndexedPropertyCheck     `json:"indexed_property_checks,omitempty"`
	SlugChecks            []repairSlugCheck                `json:"slug_checks,omitempty"`
	Warnings              []string                         `json:"warnings,omitempty"`
}

type repairIndexedPropertyCheck struct {
	PropertyName     string `json:"property_name"`
	ExpectedFromNode bool   `json:"expected_from_node"`
	MatchingRowCount int    `json:"matching_row_count"`
}

type repairSlugCheck struct {
	NodeType         string `json:"node_type"`
	Slug             string `json:"slug"`
	MatchingRowCount int    `json:"matching_row_count"`
}

func readRequiredUUIDEnv(name string) (uuid.UUID, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return uuid.Nil, fmt.Errorf("%s is required", name)
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse %s %q: %w", name, value, err)
	}
	return id, nil
}

func readRequiredStringEnv(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func readRequiredJSONEnv(name string) (json.RawMessage, error) {
	value, err := readRequiredStringEnv(name)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(value)
	if !json.Valid(raw) {
		return nil, fmt.Errorf("%s must contain valid JSON", name)
	}
	return raw, nil
}

func writeRepairOutput(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode repair output: %w", err)
	}
	return nil
}
