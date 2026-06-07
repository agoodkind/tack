# Tack

Tack is a horizontally scalable, multi-tenant project management platform: a
replacement for Plane CE, Linear, and Jira, built for production scale from the
first commit. It is not a personal tool or a prototype.

## Design philosophy

These are the durable principles that govern every decision. The binding
architecture and rules live in [AGENTS.md](AGENTS.md); the design docs and
runbooks live in [docs/](docs/). If this README and AGENTS.md ever disagree on a
rule, AGENTS.md wins.

### Everything is a node

Every entity (org, workspace, project, state, label, issue, epic, cycle, module,
comment, activity, custom type) is the same primitive: a node in FoundationDB
with universal fields and a property map, connected by relationship edges. There
are no special-case entity tables. A new kind of entity is data, not a code
change. Behavior follows node-type metadata, never hardcoded type names.

### Horizontal scale from day zero

The system is designed for N=many while it runs on one host. There is no single
point of write contention and no coordinator a second node would have to fight.
Growing from one node to many is configuration and operations, not a rewrite. We
never ship a design that "works for now" and is meant to be fixed for scale
later. If it will not scale, it does not ship.

### Over-engineered and over-optimized early, on purpose

We pay for scale-grade architecture before the load arrives, because retrofitting
scale into a live multi-tenant system is the expensive and dangerous path. Early
over-engineering here is a deliberate trade, not gold-plating. The default answer
to "is this too much for current scale" is yes, and that is intended.

### Assume millions of requests per second on every slice

Not only the hot paths. Every read, every write, and every background job is
designed as if it will be hammered, including the slices nobody predicted. There
is no path that gets a pass because "this one is low traffic." Unexpected load on
an unexpected slice is a first-class case, not an incident.

### Exotic and unexpected use cases are first-class

The type system is user-extensible. Hierarchy is a DAG defined by metadata, not a
fixed tree. References, addresses, and behavior are declared as data. Strange,
demanding, never-anticipated requirements are meant to be supported by the core
design, not bolted on at the edges. The architecture is built to absorb the crazy
case without a rewrite.

### Multi-tenant from the start

The org is the tenancy root. Tenant isolation is built into identity, storage
locality, and authorization. It is never layered on after the fact, and no
feature is designed as if there were a single tenant.

## Where to look

- [AGENTS.md](AGENTS.md): the binding contract, the configs/tack deploy seam, the
  settled decisions, and the architecture.
- [docs/plans/](docs/plans/): forward design, including the audit subsystem
  horizontal design and the Kafka cutover.
- [docs/runbooks/](docs/runbooks/): recovery and operational procedures.
- The code is the source of truth for concrete values: FDB keys in
  `internal/adapters/foundationdb/keys.go`, SQL schema in `migrations/`, config
  in `internal/config/config.go`, and the stack in `docker-compose.yml`.
