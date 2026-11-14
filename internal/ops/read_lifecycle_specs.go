package ops

import (
	"context"
	"encoding/json"
	"strings"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

const repairManifestTarget = "repair/manifest"

// singletonReadSpec returns the read definition for a command that reads one
// fixed external resource and does not record input.
func singletonReadSpec(verb audit.Verb, reference string) audit.Spec {
	target := audit.ExternalTarget(reference)

	return audit.Spec{
		Verb:  string(verb),
		Reads: true,
		Class: audit.OperationRead,
		Targets: func(context.Context, json.RawMessage) ([]audit.Target, error) {
			return []audit.Target{target}, nil
		},
		SafeInput: func(json.RawMessage) (json.RawMessage, error) { return nil, nil },
	}
}

func nodeInputTarget(nodeID string) audit.Target {
	return audit.ResourceTarget(audit.ResourceURI(audit.NodeURIPrefix, nodeID))
}

func orgInputTarget(orgID string) audit.Target {
	return audit.ResourceTarget(audit.ResourceURI(audit.OrgURIPrefix, orgID))
}

func verifyNodeSpec() audit.Spec {
	return clispec.InputReadSpec(
		audit.VerbOpsVerifyNode,
		func(in verifyNodeInput) audit.Target { return nodeInputTarget(in.NodeID) },
		func(in verifyNodeInput) verifyNodeInput {
			return verifyNodeInput{
				InputMarker: clispec.InputMarker{},
				NodeID:      audit.CanonicalUUID(in.NodeID),
				Class:       in.Class,
				Profile:     "",
				Records:     in.Records,
			}
		},
	)
}

func validateNodeSpec() audit.Spec {
	return clispec.InputReadSpec(
		audit.VerbOpsValidateNode,
		func(in validateNodeInput) audit.Target { return nodeInputTarget(in.NodeID) },
		func(in validateNodeInput) validateNodeInput {
			return validateNodeInput{
				InputMarker: clispec.InputMarker{},
				NodeID:      audit.CanonicalUUID(in.NodeID),
				Class:       in.Class,
				Profile:     "",
			}
		},
	)
}

func inspectReadSpec() audit.Spec {
	return clispec.InputReadSpec(
		audit.VerbOpsInspectRead,
		func(in inspectReadInput) audit.Target { return nodeInputTarget(in.NodeID) },
		func(in inspectReadInput) inspectReadInput {
			return inspectReadInput{
				InputMarker: clispec.InputMarker{},
				NodeID:      audit.CanonicalUUID(in.NodeID),
				Records:     in.Records,
			}
		},
	)
}

func repairPreviewSpec() audit.Spec {
	return clispec.InputReadSpec(
		audit.VerbOpsRepairPreview,
		func(in repairPreviewInput) audit.Target {
			if audit.CanonicalUUID(in.NodeID) == "" && strings.TrimSpace(in.Manifest) != "" {
				return audit.ExternalTarget(repairManifestTarget)
			}

			return nodeInputTarget(in.NodeID)
		},
		func(in repairPreviewInput) repairPreviewInput {
			return repairPreviewInput{
				InputMarker: clispec.InputMarker{},
				NodeID:      audit.CanonicalUUID(in.NodeID),
				Class:       in.Class,
				Profile:     "",
				Manifest:    "",
			}
		},
	)
}

func inspectFindSpec() audit.Spec {
	return clispec.InputReadSpec(
		audit.VerbOpsInspectFind,
		func(in inspectFindInput) audit.Target { return orgInputTarget(in.OrgID) },
		func(in inspectFindInput) inspectFindInput {
			return inspectFindInput{
				InputMarker: clispec.InputMarker{},
				OrgID:       audit.CanonicalUUID(in.OrgID),
				NodeType:    in.NodeType,
				Limit:       in.Limit,
			}
		},
	)
}

func inspectQuerySpec() audit.Spec {
	return clispec.InputReadSpec(
		audit.VerbOpsInspectQuery,
		func(in inspectQueryInput) audit.Target { return orgInputTarget(in.OrgID) },
		func(in inspectQueryInput) inspectQueryInput {
			return inspectQueryInput{
				InputMarker: clispec.InputMarker{},
				OrgID:       audit.CanonicalUUID(in.OrgID),
				NodeType:    in.NodeType,
				Property:    in.Property,
				Value:       "",
				Limit:       in.Limit,
			}
		},
	)
}
