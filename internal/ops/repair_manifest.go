package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/uuid"
)

// RepairManifest lists nodes and one profile for manifest-driven preview or apply.
type RepairManifest struct {
	Profile RepairReferenceProfile `json:"profile"`
	Nodes   []RepairManifestNode   `json:"nodes"`
}

// RepairManifestNode identifies one node and its optional apply token.
type RepairManifestNode struct {
	NodeID            uuid.UUID `json:"node_id"`
	ConfirmationToken string    `json:"confirmation_token,omitempty"`
}

// RepairManifestPreviewOutput is the deterministic JSON output for manifest previews.
type RepairManifestPreviewOutput struct {
	Command     string                  `json:"command"`
	RepairClass RepairClass             `json:"repair_class"`
	Profile     RepairReferenceProfile  `json:"profile"`
	Results     []RepairManifestPreview `json:"results"`
	SafeMode    string                  `json:"safe_mode"`
}

// RepairManifestPreview records one manifest preview result.
type RepairManifestPreview struct {
	NodeID  uuid.UUID      `json:"node_id"`
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Preview *RepairPreview `json:"preview,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// RepairManifestApplyOutput is the deterministic JSON output for manifest applies.
type RepairManifestApplyOutput struct {
	Command     string                 `json:"command"`
	RepairClass RepairClass            `json:"repair_class"`
	Profile     RepairReferenceProfile `json:"profile"`
	Results     []RepairManifestApply  `json:"results"`
	SafeMode    string                 `json:"safe_mode"`
}

// RepairManifestApply records one manifest apply result.
type RepairManifestApply struct {
	NodeID uuid.UUID          `json:"node_id"`
	Status string             `json:"status"`
	Result *RepairApplyResult `json:"result,omitempty"`
	Error  string             `json:"error,omitempty"`
}

func readRepairManifest(ctx context.Context, path string) (*RepairManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("open repair manifest %q", path), err)
	}
	defer file.Close()
	var manifest RepairManifest
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("decode repair manifest %q", path), err)
	}
	if len(manifest.Nodes) == 0 {
		return nil, fmt.Errorf("repair manifest %q has no nodes", path)
	}
	return &manifest, nil
}

func readRepairProfile(ctx context.Context, path string) (*RepairReferenceProfile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("open repair profile %q", path), err)
	}
	defer file.Close()
	var profile RepairReferenceProfile
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("decode repair profile %q", path), err)
	}
	return &profile, nil
}
