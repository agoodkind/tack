package ops

import (
	"context"
	"encoding/json"
	"strings"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
)

func runInspectRead(ctx context.Context, cfg *config.Config, argv []string) {
	flagSet := newCommandFlagSet("inspect read")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	includeRecords := flagSet.Bool("records", false, "include raw inspection rows")
	err := flagSet.Parse(argv)
	if err != nil {
		exitCommand(2)
	}
	if flagSet.NArg() != 0 {
		failCommandUsage("inspect read", "inspect read: unexpected positional arguments")
	}
	if strings.TrimSpace(*nodeIDRaw) == "" {
		failCommandUsage("inspect read", "inspect read: --node is required")
	}
	nodeID := mustParseUUIDArg("inspect read", "--node", *nodeIDRaw)
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
	if err != nil {
		failCommandf("inspect read: inspect node %s: %v", nodeID, err)
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
	if *includeRecords {
		result["records"] = report
	}
	writeCommandJSON(result)
}

func runInspectFind(ctx context.Context, cfg *config.Config, argv []string) {
	flagSet := newCommandFlagSet("inspect find")
	orgIDRaw := flagSet.String("org", "", "org UUID")
	nodeType := flagSet.String("type", "", "node type key")
	limit := flagSet.Int("limit", 50, "max rows")
	err := flagSet.Parse(argv)
	if err != nil {
		exitCommand(2)
	}
	if flagSet.NArg() != 0 {
		failCommandUsage("inspect find", "inspect find: unexpected positional arguments")
	}
	if strings.TrimSpace(*orgIDRaw) == "" {
		failCommandUsage("inspect find", "inspect find: --org is required")
	}
	if *limit <= 0 {
		failCommandUsage("inspect find", "inspect find: --limit must be greater than zero")
	}
	query := node.NodeListQuery{
		OrgID:            mustParseUUIDArg("inspect find", "--org", *orgIDRaw),
		NodeType:         *nodeType,
		ByProperty:       nil,
		BySourceRelation: nil,
		ByTargetRelation: nil,
		CreatedAfter:     nil,
		CreatedBefore:    nil,
		PropFilters:      nil,
		Limit:            *limit,
		Cursor:           "",
	}
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	views, err := env.Stores.Views.List(ctx, query)
	if err != nil {
		failCommandf("inspect find: list views: %v", err)
	}
	writeCommandJSON(map[string]any{"command": "inspect.find", "query": query, "count": len(views), "views": views})
}

func runInspectQuery(ctx context.Context, cfg *config.Config, argv []string) {
	flagSet := newCommandFlagSet("inspect query")
	orgIDRaw := flagSet.String("org", "", "org UUID")
	nodeType := flagSet.String("type", "", "node type key")
	propertyName := flagSet.String("property", "", "property name")
	propertyValue := flagSet.String("value", "", "property value as JSON")
	limit := flagSet.Int("limit", 50, "max rows")
	err := flagSet.Parse(argv)
	if err != nil {
		exitCommand(2)
	}
	if flagSet.NArg() != 0 {
		failCommandUsage("inspect query", "inspect query: unexpected positional arguments")
	}
	if strings.TrimSpace(*orgIDRaw) == "" || strings.TrimSpace(*propertyName) == "" || strings.TrimSpace(*propertyValue) == "" {
		failCommandUsage("inspect query", "inspect query: --org, --property, and --value are required")
	}
	if *limit <= 0 {
		failCommandUsage("inspect query", "inspect query: --limit must be greater than zero")
	}
	rawValue := json.RawMessage(*propertyValue)
	if !json.Valid(rawValue) {
		failCommandUsage("inspect query", "inspect query: --value must be valid JSON")
	}
	query := node.NodeListQuery{
		OrgID:            mustParseUUIDArg("inspect query", "--org", *orgIDRaw),
		NodeType:         *nodeType,
		ByProperty:       nil,
		BySourceRelation: nil,
		ByTargetRelation: nil,
		CreatedAfter:     nil,
		CreatedBefore:    nil,
		PropFilters:      []node.PropertyMatch{{PropName: *propertyName, Value: rawValue}},
		Limit:            *limit,
		Cursor:           "",
	}
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	views, err := env.Stores.Views.List(ctx, query)
	if err != nil {
		failCommandf("inspect query: list views: %v", err)
	}
	writeCommandJSON(map[string]any{"command": "inspect.query", "query": query, "count": len(views), "views": views})
}
