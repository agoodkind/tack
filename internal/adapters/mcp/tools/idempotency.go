package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
)

type mcpRequestMetadataKey struct{}

// MCPRequestMetadata carries transport-level identity that mcp-go does not
// expose on CallToolRequest.
type MCPRequestMetadata struct {
	RequestID string
	SessionID string
}

// WithMCPRequestMetadata stores transport-level request identity in context.
func WithMCPRequestMetadata(ctx context.Context, metadata MCPRequestMetadata) context.Context {
	return context.WithValue(ctx, mcpRequestMetadataKey{}, metadata)
}

type createIdempotencyInput struct {
	ToolName       string
	NodeTypeKey    string
	EntryPointSlug string
	Name           string
	ParentID       uuid.UUID
	ScopeID        uuid.UUID
	UserID         uuid.UUID
	Props          map[string]json.RawMessage
	Args           argMap
}

func createIdempotency(ctx context.Context, req mcpmcp.CallToolRequest, in createIdempotencyInput) (key string, fingerprint string, source string, err error) {
	fingerprint, err = createFingerprint(in)
	if err != nil {
		return "", "", "", err
	}

	if explicit := optionalString(in.Args, "idempotency_key"); explicit != "" {
		return explicit, fingerprint, "explicit", nil
	}

	operationID := operationIDFromMeta(req)
	sessionID := sessionIDFromRequest(ctx, req)
	if operationID == "" && sessionID != "" {
		operationID = operationIDFromContext(ctx)
	}
	if operationID == "" {
		return "", fingerprint, "", nil
	}

	return fmt.Sprintf("mcp:%s:%s:%s:%s", in.UserID, in.ToolName, sessionID, operationID), fingerprint, "mcp", nil
}

func createFingerprint(in createIdempotencyInput) (string, error) {
	payload := struct {
		ToolName       string                     `json:"tool_name"`
		NodeTypeKey    string                     `json:"node_type_key"`
		EntryPointSlug string                     `json:"entry_point_slug"`
		Name           string                     `json:"name"`
		ParentID       string                     `json:"parent_id"`
		ScopeID        string                     `json:"scope_id"`
		Props          map[string]json.RawMessage `json:"props"`
	}{
		ToolName:       in.ToolName,
		NodeTypeKey:    in.NodeTypeKey,
		EntryPointSlug: in.EntryPointSlug,
		Name:           in.Name,
		ParentID:       in.ParentID.String(),
		ScopeID:        in.ScopeID.String(),
		Props:          in.Props,
	}
	if payload.Props == nil {
		payload.Props = map[string]json.RawMessage{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal idempotency fingerprint: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func operationIDFromMeta(req mcpmcp.CallToolRequest) string {
	if req.Params.Meta == nil {
		return ""
	}
	if value, ok := req.Params.Meta.AdditionalFields["tackOperationID"]; ok {
		return fmt.Sprint(value)
	}
	return ""
}

func operationIDFromContext(ctx context.Context) string {
	metadata, ok := ctx.Value(mcpRequestMetadataKey{}).(MCPRequestMetadata)
	if !ok {
		return ""
	}
	return metadata.RequestID
}

func sessionIDFromRequest(ctx context.Context, req mcpmcp.CallToolRequest) string {
	sessionID := req.Header.Get("Mcp-Session-Id")
	if sessionID != "" {
		return sessionID
	}
	metadata, ok := ctx.Value(mcpRequestMetadataKey{}).(MCPRequestMetadata)
	if !ok {
		return ""
	}
	return metadata.SessionID
}
