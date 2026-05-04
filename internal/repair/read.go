package repair

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
)

func RunRead(cfg *config.Config, argv []string) {
	runRead(cfg, argv)
}

func runRead(cfg *config.Config, argv []string) {
	flagSet := newFlagSet("read")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	includeRecords := flagSet.Bool("records", false, "include raw inspection rows")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if flagSet.NArg() != 0 {
		failUsage("read", "read: unexpected positional arguments")
	}
	if strings.TrimSpace(*nodeIDRaw) == "" {
		failUsage("read", "read: --node is required")
	}
	nodeID := mustParseUUID("read", "--node", *nodeIDRaw)
	env := mustOpenEnv(cfg)
	defer env.Close()
	report, err := env.Stores.Inspect.QueryNodeRecords(context.Background(), nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: inspect node %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	selection := selectNode(report)
	result := map[string]any{
		"command":   "read",
		"node_id":   nodeID,
		"selection": selection,
		"resolve":   report.Resolve,
		"view":      selection.NodeView,
		"primary":   selection.Node,
	}
	if *includeRecords {
		result["records"] = report
	}
	writeJSON(result)
}

func RunFind(cfg *config.Config, argv []string) {
	runFind(cfg, argv)
}

func runFind(cfg *config.Config, argv []string) {
	flagSet := newFlagSet("find")
	orgIDRaw := flagSet.String("org", "", "org UUID")
	nodeType := flagSet.String("type", "", "node type key")
	limit := flagSet.Int("limit", 50, "max rows")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if flagSet.NArg() != 0 {
		failUsage("find", "find: unexpected positional arguments")
	}
	if strings.TrimSpace(*orgIDRaw) == "" {
		failUsage("find", "find: --org is required")
	}
	if *limit <= 0 {
		failUsage("find", "find: --limit must be greater than zero")
	}
	query := node.NodeListQuery{
		OrgID:    mustParseUUID("find", "--org", *orgIDRaw),
		NodeType: *nodeType,
		Limit:    *limit,
	}
	env := mustOpenEnv(cfg)
	defer env.Close()
	views, err := env.Stores.Views.List(context.Background(), query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "find: list views: %v\n", err)
		os.Exit(1)
	}
	writeJSON(map[string]any{"command": "find", "query": query, "count": len(views), "views": views})
}

func RunQuery(cfg *config.Config, argv []string) {
	runQuery(cfg, argv)
}

func runQuery(cfg *config.Config, argv []string) {
	flagSet := newFlagSet("query")
	orgIDRaw := flagSet.String("org", "", "org UUID")
	nodeType := flagSet.String("type", "", "node type key")
	propertyName := flagSet.String("property", "", "property name")
	propertyValue := flagSet.String("value", "", "property value as JSON")
	limit := flagSet.Int("limit", 50, "max rows")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if flagSet.NArg() != 0 {
		failUsage("query", "query: unexpected positional arguments")
	}
	if strings.TrimSpace(*orgIDRaw) == "" || strings.TrimSpace(*propertyName) == "" || strings.TrimSpace(*propertyValue) == "" {
		failUsage("query", "query: --org, --property, and --value are required")
	}
	if *limit <= 0 {
		failUsage("query", "query: --limit must be greater than zero")
	}
	rawValue := json.RawMessage(*propertyValue)
	if !json.Valid(rawValue) {
		failUsage("query", "query: --value must be valid JSON")
	}
	query := node.NodeListQuery{
		OrgID:       mustParseUUID("query", "--org", *orgIDRaw),
		NodeType:    *nodeType,
		PropFilters: []node.PropertyMatch{{PropName: *propertyName, Value: rawValue}},
		Limit:       *limit,
	}
	env := mustOpenEnv(cfg)
	defer env.Close()
	views, err := env.Stores.Views.List(context.Background(), query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query: list views: %v\n", err)
		os.Exit(1)
	}
	writeJSON(map[string]any{"command": "query", "query": query, "count": len(views), "views": views})
}
