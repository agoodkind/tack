package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/domain/node"
)

func TestCreateToolDoesNotAdvertiseIdempotencyKey(t *testing.T) {
	tool := createTool(
		&node.NodeType{TypeKey: "issue", Slug: "issue", Name: "Issue"},
		"issue",
		[]ScopeLevel{{ParamName: "project_identifier"}},
		"workspace_slug",
	)

	if _, ok := tool.InputSchema.Properties["idempotency_key"]; ok {
		t.Fatal("create schema should not advertise idempotency_key")
	}
}

func TestGettingStartedHidesIdempotencyKey(t *testing.T) {
	resolver := &Resolver{entryPointSlug: "workspace"}
	body := buildGettingStartedText(resolver, nil, nil)

	if strings.Contains(body, "`idempotency_key`") {
		t.Fatalf("getting started should not tell agents to use idempotency_key:\n%s", body)
	}
	if !strings.Contains(body, "Create retries are protected automatically") {
		t.Fatalf("getting started should explain automatic create retry safety:\n%s", body)
	}
}

func TestCreateIdempotencyUsesMetaOperationID(t *testing.T) {
	userID := uuid.New()
	req := mcpmcp.CallToolRequest{
		Params: mcpmcp.CallToolParams{
			Meta: &mcpmcp.Meta{AdditionalFields: map[string]any{"tackOperationID": "op-1"}},
		},
	}

	key, fingerprint, source, err := createIdempotency(context.Background(), req, createIdempotencyInput{
		ToolName:       "tack_create_issue",
		NodeTypeKey:    "issue",
		EntryPointSlug: "main",
		Name:           "Disk pressure",
		ParentID:       uuid.New(),
		ScopeID:        uuid.New(),
		UserID:         userID,
		Props:          map[string]json.RawMessage{"priority": mustRaw(t, "urgent")},
	})
	if err != nil {
		t.Fatalf("createIdempotency: %v", err)
	}
	if !strings.Contains(key, "op-1") || !strings.Contains(key, userID.String()) {
		t.Fatalf("key %q should include operation and user", key)
	}
	if fingerprint == "" {
		t.Fatal("fingerprint should be populated")
	}
	if source != "mcp" {
		t.Fatalf("source = %q, want mcp", source)
	}
}

func TestCreateIdempotencyUsesHiddenExplicitKey(t *testing.T) {
	key, _, source, err := createIdempotency(context.Background(), mcpmcp.CallToolRequest{}, createIdempotencyInput{
		ToolName: "tack_create_issue",
		Args:     argMap{"idempotency_key": mustRaw(t, "legacy-key")},
	})
	if err != nil {
		t.Fatalf("createIdempotency: %v", err)
	}
	if key != "legacy-key" || source != "explicit" {
		t.Fatalf("key=%q source=%q, want legacy-key explicit", key, source)
	}
}
