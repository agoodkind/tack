package tools

import (
	"context"
	"fmt"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterResources registers MCP resources. Content is generated from the
// current NodeType set so that custom types surface automatically without
// editing string literals.
func RegisterResources(s *mcpserver.MCPServer, reader node.NodeReader, resolver *Resolver, nodeTypes []*node.NodeType) {
	s.AddResource(
		mcpmcp.Resource{
			URI:         "tack://getting-started",
			Name:        "tack-getting-started",
			Description: "Orientation guide. Read once per session before calling any other Tack tool.",
			MIMEType:    "text/markdown",
		},
		func(_ context.Context, _ mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return []mcpmcp.ResourceContents{
				mcpmcp.TextResourceContents{URI: "tack://getting-started", MIMEType: "text/markdown", Text: buildGettingStartedText(resolver, nodeTypes)},
			}, nil
		},
	)
}

func buildGettingStartedText(resolver *Resolver, nodeTypes []*node.NodeType) string {
	var sb strings.Builder
	entrySlug := resolver.entryPointSlug
	entryParam := resolver.EntryPointParamName()

	fmt.Fprintln(&sb, "# Tack getting started")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Read once per session before calling any other Tack tool. Skipping this leads to wrong `node_type` strings, missing scope parameters, and failed writes.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Architecture")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Everything is a node. Every entity (org, workspace, project, state, label, issue, epic, cycle, module, comment, activity) is one row in the same storage with universal fields (`id`, `org_id`, `node_type`, `name`, `props`, audit fields). Concept-specific values live in `props`. Edges between nodes (assigned_to, labeled_with, child_of, watches, comment_of) are `Relationship` records, not fields on the node. Behavior is driven by Features declared on the NodeType, not by the node's type name.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Orientation sequence")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "1. `tack_list_workspaces` returns the workspaces you can see. Copy the `slug` of the one you want.")
	fmt.Fprintf(&sb, "2. `tack_describe_%s` with `%s=<slug>` returns the %s node, every `node_type` available in the org (with `slug`, `plural_slug`, `features`), and the direct `children` of the %s.\n", entrySlug, entryParam, entrySlug, entrySlug)
	fmt.Fprintf(&sb, "3. `tack_list_property_defs` with `%s=<slug>` returns every PropertyDef. Use this to learn valid property names and which are indexed.\n", entryParam)
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Only after these three calls do you have enough context to run a correct `tack_create_<type>`.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Available CRUD tools")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Every non-builtin NodeType gets `tack_list_<plural>`, `tack_create_<slug>`, `tack_get_<slug>`, `tack_update_<slug>`, `tack_delete_<slug>`. Current types in this server:")
	fmt.Fprintln(&sb)
	for _, nt := range nodeTypes {
		if nt.Features.Has(node.FeatureExcludeFromGenericTools) {
			continue
		}
		plural := nt.PluralSlug
		if plural == "" {
			plural = nt.Slug + "s"
		}
		fmt.Fprintf(&sb, "- `%s` (plural `%s`)", nt.Slug, plural)
		if len(nt.Features) > 0 {
			fmt.Fprintf(&sb, " features: %s", strings.Join(nt.Features, ", "))
		}
		fmt.Fprintln(&sb)
	}
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Create inputs")
	fmt.Fprintln(&sb)
	fmt.Fprintf(&sb, "- `%s` is required. Entry-point slug.\n", entryParam)
	fmt.Fprintln(&sb, "- `<parent>_identifier` is required when the type lives below a scope level (e.g. `project_identifier` for an issue).")
	fmt.Fprintln(&sb, "- `name` is required.")
	fmt.Fprintln(&sb, "- `properties` is optional, keyed by property name from `tack_list_property_defs`.")
	fmt.Fprintln(&sb, "- `idempotency_key` is optional. A retry with the same key returns the previously created node instead of creating a duplicate. Use it for bulk imports and any client that may retry on transient failure.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "### properties accepts both shapes")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Pass `properties` as a real JSON object when your client supports it. The server also accepts a JSON-encoded string of the same object; this covers MCP transports that stringify nested values. Both create and update follow the same rule.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "### parent_id can point at any node under the same scope")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "By default a new node is parented under the chain-resolved scope (e.g. an issue lands directly under its project). Set `properties.parent_id` to the UUID of a deeper container (e.g. an epic) to place the new node there. Sequence numbering stays scope-wide; an issue under an epic still gets the next project-wide identifier like `TACK-N`.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "### Default workflow states")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Every project starts with five state nodes: Backlog, Todo, In Progress, Done, Cancelled. Call `tack_list_states` with `workspace_slug` and `project_identifier` to read them, then set `properties.state_id` on issues to one of those UUIDs. `NodeType.DefaultChildren` drives this; custom types can declare their own defaults without code changes.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "`tack_get_<slug>` and `tack_update_<slug>` accept either a UUID or an identifier like `TACK-65`.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Relationships")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Use `tack_add_relationship`, `tack_remove_relationship`, `tack_list_relationships` for edges. `relation_type` is free-form. Common conventions:")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "- `assigned_to` points a node at a user.")
	fmt.Fprintln(&sb, "- `labeled_with` points a node at a label node.")
	fmt.Fprintln(&sb, "- `child_of` points a node at a container. Written automatically on create. Set manually to move between containers.")
	fmt.Fprintln(&sb, "- `watches` points a user at a node.")
	fmt.Fprintln(&sb, "- `comment_of` points a comment node at its target.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "`tack_list_relationships` with `direction=out` returns edges where the node is source. `direction=in` returns edges where it is target.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Search")
	fmt.Fprintln(&sb)
	fmt.Fprintf(&sb, "`tack_search` with `%s` and `query` runs full-text search across the org. Optional `node_type` narrows by type.\n", entryParam)
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Response format")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Tool responses are markdown text wrapped in `<success>...</success>`. Lists print as a table; single-node reads print as a labeled block. Cross-references (parent, scope, state, assignees, audit fields) come back resolved to identifiers and names, not raw UUIDs.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Pass identifiers (`TACK-65`, `tack`, `main`) back to subsequent tool calls. The server also accepts UUIDs anywhere it accepts an identifier, so a copy-pasted `id` from a single-node response still works.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Single-node responses include the raw `id:` line at the bottom for cases where you genuinely need the UUID (idempotency-keyed retries, debugging). List responses omit raw UUIDs entirely; refer to entries by identifier.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "`tack_get_properties` is the truth-escape-hatch and still returns raw JSON Props for cases where you need every value untransformed.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Common mistakes to avoid")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "- `node_type` is always the TypeKey (`issue`, not `Issue` or `issues`). Never guess. Read it from the describe call.")
	fmt.Fprintln(&sb, "- Property names are lowercase snake_case from `tack_list_property_defs`. Do not send `Priority`. Send `priority`.")
	fmt.Fprintln(&sb, "- There is no dedicated `tack_set_state` tool. State transitions are property updates: `tack_update_issue { properties: { \"state_id\": \"<uuid>\" } }`.")
	fmt.Fprintln(&sb, "- There is no dedicated `tack_assign` tool. Use `tack_add_relationship { source_id, relation_type: \"assigned_to\", target_id: <user_uuid> }`.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "## Error handling")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "`<error>` responses end with an `[LLM Instruction]` line telling you what to do next. Follow it rather than retrying the same call.")
	return sb.String()
}
