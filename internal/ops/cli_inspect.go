package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/domain/node"
)

// inspectReadResult is the JSON payload for a single-node inspect read.
type inspectReadResult struct {
	clispec.ResultMarker
	Command   string                           `json:"command"`
	NodeID    uuid.UUID                        `json:"node_id"`
	Selection *repairSelectedNode              `json:"selection"`
	Resolve   *node.NodeResolve                `json:"resolve"`
	View      *node.NodeView                   `json:"view"`
	Primary   *node.Node                       `json:"primary"`
	Records   *fdbadapter.NodeInspectionReport `json:"records,omitempty"`
}

// inspectListResult is the JSON payload shared by inspect find and inspect query.
type inspectListResult struct {
	clispec.ResultMarker
	Command string             `json:"command"`
	Query   node.NodeListQuery `json:"query"`
	Count   int                `json:"count"`
	Views   []*node.NodeView   `json:"views"`
}

type inspectReadInput struct {
	clispec.InputMarker
	NodeID  string
	Records bool
}

func inspectReadOp(f *cli.Factory) clispec.Operation[inspectReadInput] {
	return clispec.Operation[inspectReadInput]{
		Name:     clispec.Name{Canonical: "read", CLIOverride: ""},
		Group:    inspectGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Read one node, including resolve, view, and primary payloads",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[inspectReadInput]{
			clispec.StringParam("node", "node UUID", "", true, func(in *inspectReadInput, v string) { in.NodeID = v }),
			clispec.BoolParam("records", "include raw inspection rows", false, func(in *inspectReadInput, v bool) { in.Records = v }),
		},
		New: func() inspectReadInput {
			return inspectReadInput{InputMarker: clispec.InputMarker{}, NodeID: "", Records: false}
		},
		Run: func(ctx context.Context, in inspectReadInput, sink clispec.ResultSink) error {
			nodeID, err := uuid.Parse(strings.TrimSpace(in.NodeID))
			if err != nil {
				slog.ErrorContext(ctx, "inspect.read_bad_node", slog.String("err", err.Error()))
				return fmt.Errorf("inspect read: --node must be a UUID: %w", err)
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
			if err != nil {
				slog.ErrorContext(ctx, "inspect.read_query_failed", slog.String("err", err.Error()))
				return fmt.Errorf("inspect read: inspect node %s: %w", nodeID, err)
			}
			selection := selectRepairNode(report)
			result := inspectReadResult{
				ResultMarker: clispec.ResultMarker{},
				Command:      "inspect.read",
				NodeID:       nodeID,
				Selection:    selection,
				Resolve:      report.Resolve,
				View:         selection.NodeView,
				Primary:      selection.Node,
				Records:      nil,
			}
			if in.Records {
				result.Records = report
			}
			return clispec.WriteJSONValue(ctx, sink, result)
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
		Name:     clispec.Name{Canonical: "find", CLIOverride: ""},
		Group:    inspectGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "List node views by org and optional node type",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[inspectFindInput]{
			clispec.StringParam("org", "org UUID", "", true, func(in *inspectFindInput, v string) { in.OrgID = v }),
			clispec.StringParam("type", "node type key", "", false, func(in *inspectFindInput, v string) { in.NodeType = v }),
			clispec.IntParam("limit", "max rows", 50, func(in *inspectFindInput, v int) { in.Limit = v }),
		},
		New: func() inspectFindInput {
			return inspectFindInput{InputMarker: clispec.InputMarker{}, OrgID: "", NodeType: "", Limit: 50}
		},
		Run: func(ctx context.Context, in inspectFindInput, sink clispec.ResultSink) error {
			if in.Limit <= 0 {
				return errors.New("inspect find: --limit must be greater than zero")
			}
			orgID, err := uuid.Parse(strings.TrimSpace(in.OrgID))
			if err != nil {
				slog.ErrorContext(ctx, "inspect.find_bad_org", slog.String("err", err.Error()))
				return fmt.Errorf("inspect find: --org must be a UUID: %w", err)
			}
			query := node.NodeListQuery{
				OrgID:            orgID,
				NodeType:         in.NodeType,
				ByProperty:       nil,
				BySourceRelation: nil,
				ByTargetRelation: nil,
				CreatedAfter:     nil,
				CreatedBefore:    nil,
				PropFilters:      nil,
				Limit:            in.Limit,
				Cursor:           "",
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			views, err := env.Stores.Views.List(ctx, query)
			if err != nil {
				slog.ErrorContext(ctx, "inspect.find_list_failed", slog.String("err", err.Error()))
				return fmt.Errorf("inspect find: list views: %w", err)
			}
			return clispec.WriteJSONValue(ctx, sink, inspectListResult{
				ResultMarker: clispec.ResultMarker{},
				Command:      "inspect.find",
				Query:        query,
				Count:        len(views),
				Views:        views,
			})
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
		Name:     clispec.Name{Canonical: "query", CLIOverride: ""},
		Group:    inspectGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Filter node views by indexed property equality",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[inspectQueryInput]{
			clispec.StringParam("org", "org UUID", "", true, func(in *inspectQueryInput, v string) { in.OrgID = v }),
			clispec.StringParam("type", "node type key", "", false, func(in *inspectQueryInput, v string) { in.NodeType = v }),
			clispec.StringParam("property", "property name", "", true, func(in *inspectQueryInput, v string) { in.Property = v }),
			clispec.StringParam("value", "property value as JSON", "", true, func(in *inspectQueryInput, v string) { in.Value = v }),
			clispec.IntParam("limit", "max rows", 50, func(in *inspectQueryInput, v int) { in.Limit = v }),
		},
		New: func() inspectQueryInput {
			return inspectQueryInput{InputMarker: clispec.InputMarker{}, OrgID: "", NodeType: "", Property: "", Value: "", Limit: 50}
		},
		Run: func(ctx context.Context, in inspectQueryInput, sink clispec.ResultSink) error {
			if in.Limit <= 0 {
				return errors.New("inspect query: --limit must be greater than zero")
			}
			rawValue := json.RawMessage(in.Value)
			if !json.Valid(rawValue) {
				return errors.New("inspect query: --value must be valid JSON")
			}
			orgID, err := uuid.Parse(strings.TrimSpace(in.OrgID))
			if err != nil {
				slog.ErrorContext(ctx, "inspect.query_bad_org", slog.String("err", err.Error()))
				return fmt.Errorf("inspect query: --org must be a UUID: %w", err)
			}
			query := node.NodeListQuery{
				OrgID:            orgID,
				NodeType:         in.NodeType,
				ByProperty:       nil,
				BySourceRelation: nil,
				ByTargetRelation: nil,
				CreatedAfter:     nil,
				CreatedBefore:    nil,
				PropFilters:      []node.PropertyMatch{{PropName: in.Property, Value: rawValue}},
				Limit:            in.Limit,
				Cursor:           "",
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			views, err := env.Stores.Views.List(ctx, query)
			if err != nil {
				slog.ErrorContext(ctx, "inspect.query_list_failed", slog.String("err", err.Error()))
				return fmt.Errorf("inspect query: list views: %w", err)
			}
			return clispec.WriteJSONValue(ctx, sink, inspectListResult{
				ResultMarker: clispec.ResultMarker{},
				Command:      "inspect.query",
				Query:        query,
				Count:        len(views),
				Views:        views,
			})
		},
	}
}
