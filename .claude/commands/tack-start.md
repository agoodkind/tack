---
description: Orient on Tack MCP. List workspaces, describe one, learn the node types and property defs in scope before any create or update.
---

# Tack getting started

Read this once per session before calling any other Tack MCP tool. Skipping it leads to wrong `node_type` strings, missing required scope parameters, and failed writes.

## Architecture in one paragraph

Everything is a node. Orgs, workspaces, projects, states, labels, issues, epics, cycles, modules, comments, activities are all nodes. Nodes have universal fields (`id`, `org_id`, `node_type`, `name`, `props`, audit fields) plus a free-form `props` map. Edges between nodes (assigned_to, labeled_with, child_of, watches, comment_of) are `Relationship` records, not fields on the node. Behavior is driven by Features declared on the NodeType, not by the node's type name.

## Orientation sequence

1. `tack_list_workspaces` returns the workspaces you can see. Copy the `slug` of the one you want.
2. `tack_describe_workspace` with `workspace_slug=<slug>` returns the `workspace` node, every `node_type` available in this org (with `slug`, `plural_slug`, `features`), and the direct `children` of the workspace (typically projects and labels).
3. `tack_list_property_defs` with `workspace_slug=<slug>` returns every PropertyDef in the org. Use this to learn which property names are valid (priority, due_date, state_id, and so on) and which are indexed.

Only after these three calls do you have enough to run a correct `tack_create_<type>`.

## CRUD pattern

Every non-builtin NodeType gets `tack_list_<plural>`, `tack_create_<slug>`, `tack_get_<slug>`, `tack_update_<slug>`, `tack_delete_<slug>`. Built-in scope types like `org` and `workspace` are excluded from generic CRUD on purpose.

`tack_create_<slug>` inputs:

- `workspace_slug` is required. It is the entry-point slug.
- `<parent>_identifier` is required when the type lives below a scope level. For an issue under a project, pass `project_identifier`.
- `name` is required.
- `properties` is an optional object keyed by property name from `tack_list_property_defs`.

Example:

```
tack_create_issue
  workspace_slug: main
  project_identifier: TACK
  name: "Audit MCP tool names"
  properties: { "priority": "high", "due_date": "2026-04-30T00:00:00Z" }
```

`tack_get_<slug>` and `tack_update_<slug>` accept either a UUID or an identifier like `TACK-65`.

## Relationships

Use `tack_add_relationship`, `tack_remove_relationship`, and `tack_list_relationships` for edges between nodes. `relation_type` is free-form. Common conventions:

- `assigned_to` points a node at a user.
- `labeled_with` points a node at a label node.
- `child_of` points a node at a container. It is written automatically on create. Set it manually when moving between containers.
- `watches` points a user at a node.
- `comment_of` points a comment node at its target.

`tack_list_relationships` with `direction=out` returns edges where the node is source. `direction=in` returns edges where it is target.

## Search

`tack_search` with `workspace_slug` and `query` runs full-text search across the org. An optional `node_type` filter narrows by type. Results are the indexed NodeView records. Fetch the full node via `tack_get_<slug>` or `tack_get_properties` if you need the full `props` map.

## What NOT to assume

- `node_type` is always the TypeKey. Use `issue`, not `Issue` or `issues`. Never guess. Read it from `tack_describe_workspace`.
- Property names are lowercase snake_case. They come from `tack_list_property_defs`. Do not send `Priority`. Send `priority`.
- There is no dedicated `tack_set_state` tool. State transitions are property updates: `tack_update_issue { properties: { "state_id": "<uuid>" } }`.
- There is no dedicated `tack_assign` tool. Use `tack_add_relationship { source_id, relation_type: "assigned_to", target_id: <user_uuid> }`.

## If a tool returns an error

`<error>` responses end with an `[LLM Instruction]` line telling you what to do next. Follow it rather than retrying the same call.
