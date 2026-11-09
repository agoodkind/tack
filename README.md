# Tack

Tack is a project management platform for many tenants and horizontal scale. It
aims to replace Plane CE, Linear, and Jira in production use. The project treats
production scale as a design requirement rather than a later addition.

[AGENTS.md](AGENTS.md) defines the binding architecture and development contract.
Its requirements take precedence if this introduction conflicts with them.

## Design requirements

Every product entity uses the same FoundationDB node model, with universal
fields and a property map. Relationships connect nodes. Node-type metadata
defines behavior without separate tables or hardcoded behavior for each entity
type. Add new entity types through metadata and data without changing
application code.

Product entities:

- Organizations use nodes.
- Workspaces use nodes.
- Projects use nodes.
- States use nodes.
- Labels use nodes.
- Issues use nodes.
- Epics use nodes.
- Cycles use nodes.
- Modules use nodes.
- Comments use nodes.
- Activities use nodes.
- Custom types use nodes.

Design each component for several instances even when an environment starts
with one host. Avoid a single point of write contention or a coordinator that
prevents another instance from writing. Additional instances must require
configuration and operations rather than an architectural rewrite. Do not ship
a design that defers required scale support.

Accept early architecture and optimization costs when they meet these scale
requirements. Replacing storage or coordination designs after tenants depend
on them adds migration cost and operational risk.

Design every read, write, and background job for a target of millions of
requests per second. Apply this requirement to expected low-traffic operations
as well as known busy operations. Account for unexpected demand in the design.
This target states a requirement; it does not report measured throughput.

Support user-defined types and demanding use cases through metadata. The type
hierarchy permits several parent types without cycles. A node that needs a
parent has one canonical direct parent. Its [reference and numbering scope](docs/reference/nodes/2026-10-16-node-integrity.md#scope-containment-and-deletion)
can differ from that parent. Declare references, addresses, and behavior as data.
New use cases must use the core model without an architectural rewrite.

Use the organization as the tenancy root. Require tenant isolation in identity,
storage locality, and authorization from the start. Design every feature for
multiple tenants.

## Where to look

Use the [project documentation](docs/README.md) for audit design, recovery
procedures, and search acceptance tests, including native runner setup, process
and cluster checks, and fixture cleanup. Use the project contract to check
current keys, SQL schemas, configuration, and stack values.
