package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
)

// orgSource selects how the recorder derives the organizations of an authenticated read.
type orgSource int

const (
	// orgSystem marks an operation that does not read tenant data.
	orgSystem orgSource = iota
	// orgCaller marks an operation with a response that includes data from every member organization.
	orgCaller
	// orgArguments marks an operation with arguments that identify one workspace or node.
	orgArguments
)

const (
	// nodeIDArgument is the tool argument that identifies a node.
	nodeIDArgument = "node_id"
	// nodeTypeTargetType and propertyDefTargetType are the node types of metadata targets.
	nodeTypeTargetType    = "node_type"
	propertyDefTargetType = "property_def"
)

type metadataTargetsKey struct{}

// withMetadataTargets marks ctx. The MCP handler then adds the node types and
// property definitions of the tool server that answers the request.
func withMetadataTargets(ctx context.Context) context.Context {
	return context.WithValue(ctx, metadataTargetsKey{}, true)
}

// addMetadataTargets adds every node type and property definition of server to
// the read identities when the request recorder marked ctx with withMetadataTargets.
func addMetadataTargets(ctx context.Context, server *cachedServer) {
	if marked, _ := ctx.Value(metadataTargetsKey{}).(bool); !marked {
		return
	}
	for _, nodeType := range server.nodeTypes {
		audit.AddReadTargets(ctx, nodeType.OrgID, metadataTarget(nodeTypeTargetType, nodeType.ID))
	}
	for _, definition := range server.propertyDefs {
		audit.AddReadTargets(ctx, definition.OrgID, metadataTarget(propertyDefTargetType, definition.ID))
	}
}

// metadataTarget returns the node target of one metadata record.
func metadataTarget(nodeType string, id uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindNode, NodeType: nodeType, Reference: "", Name: "", URI: "",
		RelationType: "", ID: id, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// requestOrganizations returns the organizations of an authenticated read before the read runs.
// An operation that does not read tenant data uses the system organization.
// An operation with data from every member organization uses each member organization.
// A tool call uses the organization of the workspace or node that its arguments identify.
// It uses the system organization if the arguments do not identify a workspace or node
// that the caller can read.
func (h *Handler) requestOrganizations(ctx context.Context, operation requestOperation) ([]uuid.UUID, error) {
	if operation.organizations == orgSystem {
		return []uuid.UUID{audit.SystemOrgID()}, nil
	}
	userID, _ := auth.UserID(ctx)
	orgIDs, attached := auth.OrgMembership(ctx)
	if !attached {
		listed, err := h.members.ListOrgIDsForUser(ctx, userID)
		if err != nil {
			slog.ErrorContext(ctx, "mcp.audit_request.membership_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("list the organizations of user %s: %w", userID, err)
		}
		orgIDs = listed
	}
	if operation.organizations == orgCaller {
		if len(orgIDs) == 0 {
			return []uuid.UUID{audit.SystemOrgID()}, nil
		}
		return orgIDs, nil
	}
	server, err := h.userServer(ctx, trace.SpanFromContext(ctx), userID, orgIDs)
	if err != nil {
		return nil, err
	}
	orgID, err := h.argumentOrganization(auth.WithOrgMembership(ctx, orgIDs), server, operation.arguments)
	if err != nil {
		return nil, err
	}
	return []uuid.UUID{orgID}, nil
}

// argumentOrganization returns the organization of the workspace or node that arguments identify.
// It returns the system organization when arguments do not identify either, or the resolver
// cannot find the workspace or node among the caller's organizations.
func (h *Handler) argumentOrganization(ctx context.Context, server *cachedServer, arguments json.RawMessage) (uuid.UUID, error) {
	values := map[string]json.RawMessage{}
	if len(arguments) != 0 {
		if err := json.Unmarshal(arguments, &values); err != nil {
			values = map[string]json.RawMessage{}
		}
	}
	if reference := stringArgument(values, server.resolver.EntryPointParamName()); reference != "" {
		workspace, err := server.resolver.Workspace(ctx, reference)
		switch {
		case err == nil && workspace != nil:
			return workspace.OrgID, nil
		case err != nil && !targetMissing(err):
			return uuid.Nil, organizationReadFailure(ctx, "workspace "+reference, err)
		}
	}
	input := stringArgument(values, nodeIDArgument)
	if input == "" {
		return audit.SystemOrgID(), nil
	}
	nodeID, err := server.resolver.ResolveNodeID(ctx, input)
	if err != nil {
		return unresolvedOrganization(ctx, "node "+input, err)
	}
	view, err := h.reader.Get(ctx, nodeID)
	if err != nil {
		return unresolvedOrganization(ctx, "node "+nodeID.String(), err)
	}
	if view == nil {
		return audit.SystemOrgID(), nil
	}
	return view.OrgID, nil
}

// targetMissing reports whether err means that the caller cannot read the named
// workspace or node, as distinct from a failed read.
func targetMissing(err error) bool {
	return errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidArgument)
}

// unresolvedOrganization returns the system organization when the caller cannot
// read target. It returns a wrapped error when the read of target failed.
func unresolvedOrganization(ctx context.Context, target string, cause error) (uuid.UUID, error) {
	if targetMissing(cause) {
		return audit.SystemOrgID(), nil
	}
	return uuid.Nil, organizationReadFailure(ctx, target, cause)
}

// organizationReadFailure logs and wraps a failed read of the organization of target.
func organizationReadFailure(ctx context.Context, target string, err error) error {
	slog.ErrorContext(ctx, "mcp.audit_request.organization_read_failed", slog.String("err", err.Error()))
	return fmt.Errorf("read the organization of %s: %w", target, err)
}

// stringArgument returns the string value of name in values, or an empty
// string when the value is absent or not a JSON string.
func stringArgument(values map[string]json.RawMessage, name string) string {
	var value string
	if json.Unmarshal(values[name], &value) != nil {
		return ""
	}
	return value
}
