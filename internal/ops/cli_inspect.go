package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/domain/node"
)

type inspectReadInput struct {
	clispec.InputMarker
	NodeID  string
	Records bool
}

func inspectReadOp(f *cli.Factory) clispec.Operation[inspectReadInput] {
	return clispec.Operation[inspectReadInput]{
		Name:  clispec.Name{Canonical: "read"},
		Group: inspectGroup,
		Short: "Read one node, including resolve, view, and primary payloads",
		Params: []clispec.Param[inspectReadInput]{
			clispec.StringParam("node", "node UUID", "", true, func(in *inspectReadInput, v string) { in.NodeID = v }),
			clispec.BoolParam("records", "include raw inspection rows", false, func(in *inspectReadInput, v bool) { in.Records = v }),
		},
		New: func() inspectReadInput { return inspectReadInput{} },
		Run: func(ctx context.Context, in inspectReadInput, sink clispec.ResultSink) error {
			nodeID, err := uuid.Parse(strings.TrimSpace(in.NodeID))
			if err != nil {
				return fmt.Errorf("inspect read: --node must be a UUID: %w", err)
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
			if err != nil {
				return fmt.Errorf("inspect read: inspect node %s: %w", nodeID, err)
			}
			selection := selectRepairNode(report)
			result := map[string]any{
				"command":   "inspect.read",
				"node_id":   nodeID,
				"selection": selection,
				"resolve":   report.Resolve,
				"view":      selection.NodeView,
				"primary":   selection.Node,
			}
			if in.Records {
				result["records"] = report
			}
			return sink.Emit(ctx, result)
		},
	}
}

type inspectFindInput struct {
	clispec.InputMarker
	OrgID    string
	NodeType string
	Limit    int
}

func inspectFindOp(f *cli.Factory) clispec.Operation[inspectFindInput] {
	return clispec.Operation[inspectFindInput]{
		Name:  clispec.Name{Canonical: "find"},
		Group: inspectGroup,
		Short: "List node views by org and optional node type",
		Params: []clispec.Param[inspectFindInput]{
			clispec.StringParam("org", "org UUID", "", true, func(in *inspectFindInput, v string) { in.OrgID = v }),
			clispec.StringParam("type", "node type key", "", false, func(in *inspectFindInput, v string) { in.NodeType = v }),
			clispec.IntParam("limit", "max rows", 50, func(in *inspectFindInput, v int) { in.Limit = v }),
		},
		New: func() inspectFindInput { return inspectFindInput{Limit: 50} },
		Run: func(ctx context.Context, in inspectFindInput, sink clispec.ResultSink) error {
			if in.Limit <= 0 {
				return fmt.Errorf("inspect find: --limit must be greater than zero")
			}
			orgID, err := uuid.Parse(strings.TrimSpace(in.OrgID))
			if err != nil {
				return fmt.Errorf("inspect find: --org must be a UUID: %w", err)
			}
			query := node.NodeListQuery{OrgID: orgID, NodeType: in.NodeType, Limit: in.Limit}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			views, err := env.Stores.Views.List(ctx, query)
			if err != nil {
				return fmt.Errorf("inspect find: list views: %w", err)
			}
			return sink.Emit(ctx, map[string]any{"command": "inspect.find", "query": query, "count": len(views), "views": views})
		},
	}
}

type inspectQueryInput struct {
	clispec.InputMarker
	OrgID    string
	NodeType string
	Property string
	Value    string
	Limit    int
}

func inspectQueryOp(f *cli.Factory) clispec.Operation[inspectQueryInput] {
	return clispec.Operation[inspectQueryInput]{
		Name:  clispec.Name{Canonical: "query"},
		Group: inspectGroup,
		Short: "Filter node views by indexed property equality",
		Params: []clispec.Param[inspectQueryInput]{
			clispec.StringParam("org", "org UUID", "", true, func(in *inspectQueryInput, v string) { in.OrgID = v }),
			clispec.StringParam("type", "node type key", "", false, func(in *inspectQueryInput, v string) { in.NodeType = v }),
			clispec.StringParam("property", "property name", "", true, func(in *inspectQueryInput, v string) { in.Property = v }),
			clispec.StringParam("value", "property value as JSON", "", true, func(in *inspectQueryInput, v string) { in.Value = v }),
			clispec.IntParam("limit", "max rows", 50, func(in *inspectQueryInput, v int) { in.Limit = v }),
		},
		New: func() inspectQueryInput { return inspectQueryInput{Limit: 50} },
		Run: func(ctx context.Context, in inspectQueryInput, sink clispec.ResultSink) error {
			if in.Limit <= 0 {
				return fmt.Errorf("inspect query: --limit must be greater than zero")
			}
			rawValue := json.RawMessage(in.Value)
			if !json.Valid(rawValue) {
				return fmt.Errorf("inspect query: --value must be valid JSON")
			}
			orgID, err := uuid.Parse(strings.TrimSpace(in.OrgID))
			if err != nil {
				return fmt.Errorf("inspect query: --org must be a UUID: %w", err)
			}
			query := node.NodeListQuery{
				OrgID:       orgID,
				NodeType:    in.NodeType,
				PropFilters: []node.PropertyMatch{{PropName: in.Property, Value: rawValue}},
				Limit:       in.Limit,
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			views, err := env.Stores.Views.List(ctx, query)
			if err != nil {
				return fmt.Errorf("inspect query: list views: %w", err)
			}
			return sink.Emit(ctx, map[string]any{"command": "inspect.query", "query": query, "count": len(views), "views": views})
		},
	}
}
