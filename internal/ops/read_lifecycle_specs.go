package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/audit"
)

const repairManifestTarget = "repair/manifest"

type nodeReadFlags struct {
	NodeID   string `json:"NodeID"`
	Class    string `json:"Class"`
	Records  bool   `json:"Records"`
	Manifest string `json:"Manifest"`
}

type nodeReadSafeInput struct {
	NodeID  string `json:"node_id"`
	Class   string `json:"class,omitempty"`
	Records bool   `json:"records,omitempty"`
}

type orgReadFlags struct {
	OrgID    string `json:"OrgID"`
	NodeType string `json:"NodeType"`
	Property string `json:"Property"`
	Limit    int    `json:"Limit"`
}

type orgReadSafeInput struct {
	OrgID    string `json:"org_id"`
	NodeType string `json:"node_type,omitempty"`
	Property string `json:"property,omitempty"`
	Limit    int    `json:"limit"`
}

func decodeReadFlags[Flags nodeReadFlags | orgReadFlags](verb audit.Verb, encoded json.RawMessage) (Flags, error) {
	var decoded Flags
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		slog.Error("ops.read_input_failed", slog.String("verb", string(verb)), slog.String("err", err.Error()))
		return decoded, fmt.Errorf("%s: decode the command input: %w", verb, err)
	}
	return decoded, nil
}

func encodeReadSafeInput[Safe nodeReadSafeInput | orgReadSafeInput](verb audit.Verb, safe Safe) (json.RawMessage, error) {
	encoded, err := json.Marshal(safe)
	if err != nil {
		slog.Error("ops.read_input_failed", slog.String("verb", string(verb)), slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s: encode the safe input: %w", verb, err)
	}
	return encoded, nil
}

// singletonReadSpec returns the read definition for a command that reads one
// fixed external resource and does not record input.
func singletonReadSpec(verb audit.Verb, reference string) audit.Spec {
	target := externalTarget(reference)
	return audit.Spec{
		Verb: string(verb), Reads: true, Class: audit.OperationRead,
		Targets: func(context.Context, json.RawMessage) ([]audit.Target, error) {
			return []audit.Target{target}, nil
		},
		SafeInput: func(json.RawMessage) (json.RawMessage, error) { return nil, nil },
	}
}

// nodeReadSpec returns the read definition for a command that reads one node.
// When the command reads a repair manifest instead, the definition targets the
// manifest reference.
func nodeReadSpec(verb audit.Verb) audit.Spec {
	return audit.Spec{
		Verb: string(verb), Reads: true, Class: audit.OperationRead,
		Targets: func(_ context.Context, encoded json.RawMessage) ([]audit.Target, error) {
			flags, err := decodeReadFlags[nodeReadFlags](verb, encoded)
			if err != nil {
				return nil, err
			}
			if audit.CanonicalUUID(flags.NodeID) == "" && strings.TrimSpace(flags.Manifest) != "" {
				return []audit.Target{externalTarget(repairManifestTarget)}, nil
			}
			return []audit.Target{audit.ResourceTarget(audit.ResourceURI(audit.NodeURIPrefix, flags.NodeID))}, nil
		},
		SafeInput: func(encoded json.RawMessage) (json.RawMessage, error) {
			flags, err := decodeReadFlags[nodeReadFlags](verb, encoded)
			if err != nil {
				return nil, err
			}
			return encodeReadSafeInput(verb, nodeReadSafeInput{NodeID: audit.CanonicalUUID(flags.NodeID), Class: flags.Class, Records: flags.Records})
		},
	}
}

// orgReadSpec returns the read definition for a command that lists the node
// views of one organization.
func orgReadSpec(verb audit.Verb) audit.Spec {
	return audit.Spec{
		Verb: string(verb), Reads: true, Class: audit.OperationRead,
		Targets: func(_ context.Context, encoded json.RawMessage) ([]audit.Target, error) {
			flags, err := decodeReadFlags[orgReadFlags](verb, encoded)
			if err != nil {
				return nil, err
			}
			return []audit.Target{audit.ResourceTarget(audit.ResourceURI(audit.OrgURIPrefix, flags.OrgID))}, nil
		},
		SafeInput: func(encoded json.RawMessage) (json.RawMessage, error) {
			flags, err := decodeReadFlags[orgReadFlags](verb, encoded)
			if err != nil {
				return nil, err
			}
			return encodeReadSafeInput(verb, orgReadSafeInput{
				OrgID: audit.CanonicalUUID(flags.OrgID), NodeType: flags.NodeType, Property: flags.Property, Limit: flags.Limit,
			})
		},
	}
}

func nodeResourceTarget(nodeID string) audit.Target {
	return audit.ResourceTarget(audit.ResourceURI(audit.NodeURIPrefix, nodeID))
}
