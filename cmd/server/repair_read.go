package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
)

func runRepairRead(cfg *config.Config, argv []string) {
	flagSet := newRepairFlagSet("read")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *nodeIDRaw == "" {
		fmt.Fprintln(os.Stderr, "read: --node is required")
		os.Exit(2)
	}
	nodeID := mustParseUUID("read", "--node", *nodeIDRaw)
	env := mustOpenRepairEnv(cfg)
	defer env.Close()
	resolve, err := env.Stores.Views.Resolve(context.Background(), nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: resolve node %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	view, err := env.Stores.Views.Get(context.Background(), nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: get view %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	primary, err := env.Stores.Nodes.Get(context.Background(), resolve.OrgID, nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: get primary node %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	writeRepairJSON(map[string]any{"node_id": nodeID, "resolve": resolve, "view": view, "primary": primary})
}

func runRepairFind(cfg *config.Config, argv []string) {
	flagSet := newRepairFlagSet("find")
	orgIDRaw := flagSet.String("org", "", "org UUID")
	nodeType := flagSet.String("type", "", "node type key")
	limit := flagSet.Int("limit", 50, "max rows")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *orgIDRaw == "" {
		fmt.Fprintln(os.Stderr, "find: --org is required")
		os.Exit(2)
	}
	query := node.NodeListQuery{OrgID: mustParseUUID("find", "--org", *orgIDRaw), NodeType: *nodeType, Limit: *limit}
	env := mustOpenRepairEnv(cfg)
	defer env.Close()
	views, err := env.Stores.Views.List(context.Background(), query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "find: list views: %v\n", err)
		os.Exit(1)
	}
	writeRepairJSON(map[string]any{"query": query, "count": len(views), "views": views})
}

func runRepairQuery(cfg *config.Config, argv []string) {
	flagSet := newRepairFlagSet("query")
	orgIDRaw := flagSet.String("org", "", "org UUID")
	nodeType := flagSet.String("type", "", "node type key")
	propertyName := flagSet.String("property", "", "property name")
	propertyValue := flagSet.String("value", "", "property value as JSON")
	limit := flagSet.Int("limit", 50, "max rows")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *orgIDRaw == "" || *propertyName == "" || *propertyValue == "" {
		fmt.Fprintln(os.Stderr, "usage: ./server repair query --org <uuid> --property <name> --value <json> [--type <type>] [--limit N]")
		os.Exit(2)
	}
	rawValue := json.RawMessage(*propertyValue)
	if !json.Valid(rawValue) {
		fmt.Fprintln(os.Stderr, "query: --value must be valid JSON")
		os.Exit(2)
	}
	query := node.NodeListQuery{
		OrgID:       mustParseUUID("query", "--org", *orgIDRaw),
		NodeType:    *nodeType,
		PropFilters: []node.PropertyMatch{{PropName: *propertyName, Value: rawValue}},
		Limit:       *limit,
	}
	env := mustOpenRepairEnv(cfg)
	defer env.Close()
	views, err := env.Stores.Views.List(context.Background(), query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query: list views: %v\n", err)
		os.Exit(1)
	}
	writeRepairJSON(map[string]any{"query": query, "count": len(views), "views": views})
}

func runRepairVerify(cfg *config.Config, argv []string) {
	flagSet := newRepairFlagSet("verify")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *nodeIDRaw == "" {
		fmt.Fprintln(os.Stderr, "verify: --node is required")
		os.Exit(2)
	}
	nodeID := mustParseUUID("verify", "--node", *nodeIDRaw)
	env := mustOpenRepairEnv(cfg)
	defer env.Close()
	resolve, resolveErr := env.Stores.Views.Resolve(context.Background(), nodeID)
	view, viewErr := env.Stores.Views.Get(context.Background(), nodeID)
	var primaryNodeID uuid.UUID
	primaryFound := false
	if resolveErr == nil && resolve != nil {
		primary, primaryErr := env.Stores.Nodes.Get(context.Background(), resolve.OrgID, nodeID)
		if primaryErr == nil && primary != nil {
			primaryNodeID = primary.ID
			primaryFound = true
		}
	}
	writeRepairJSON(map[string]any{
		"node_id":       nodeID,
		"resolve_found": resolveErr == nil && resolve != nil,
		"view_found":    viewErr == nil && view != nil,
		"primary_found": primaryFound,
		"org_matches":   resolveErr == nil && resolve != nil && viewErr == nil && view != nil && resolve.OrgID == view.OrgID,
		"type_matches":  resolveErr == nil && resolve != nil && viewErr == nil && view != nil && resolve.NodeType == view.NodeType,
		"primary_id":    primaryNodeID,
		"resolve_error": errorString(resolveErr),
		"view_error":    errorString(viewErr),
	})
}

func errorString(input error) string {
	if input == nil {
		return ""
	}
	return input.Error()
}
