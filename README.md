# Tack

Tack is a horizontally scalable, multi-tenant project management platform: a
replacement for Plane CE, Linear, and Jira. It is not a personal tool or a
prototype.

## Design philosophy

[AGENTS.md](AGENTS.md) defines the binding architecture and rules and takes
precedence over this README. [docs/](docs/) contains design documents and runbooks.

### Everything is a node

Every entity (org, workspace, project, state, label, issue, epic, cycle, module,
comment, activity, custom type) is a node in FoundationDB with universal fields
and a property map. Relationship edges connect nodes. The system does not use
special-case entity tables. Metadata defines new entity kinds and their behavior,
never hardcoded type names.

### Design for horizontal scale

Design for many nodes even when running on one host. Avoid a single point of
write contention or a coordinator that limits additional nodes. Expansion must
require configuration and operations, not a rewrite. Do not ship designs that
require later changes to support scale.

### Accept early complexity for scale

Choose architecture for expected scale before demand increases, even when it
requires more engineering than current demand alone would justify.

### Design every operation for high request rates

Design every read, write, and background job for millions of requests per second,
including operations that currently receive little traffic. Account for
unexpected demand on any operation.

### Support unexpected use cases

Users can extend the type system. Metadata defines hierarchy as a DAG rather
than a fixed tree, and defines references, addresses, and behavior. Support
unusual requirements through the core design without a rewrite.

### Design every feature for multiple tenants

The org is the tenancy root. Include tenant isolation in identity, storage
locality, and authorization. Do not design features for a single tenant or defer
isolation until later.

## Server diagnostics

Read `tack_http_requests_total` at `GET /debug/vars` to see how many HTTP requests that server instance has received since startup. This count includes requests to the diagnostics endpoint.

To export the request count as `tack.http.requests`, set `GKLOG_METRIC_EXPORT_ENABLED=true` and configure an OTLP gRPC collector with `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT`. A metric exemplar can identify the sampled request trace.

The server reads diagnostic settings at startup. Use `GKLOG_LOG_ENABLED` and `GKLOG_LOG_LEVEL` for logging. Use `GKLOG_TRACE_ENABLED` and `GKLOG_TRACE_SAMPLE_RATIO` for new traces. To collect CPU profiles, set `PYROSCOPE_SERVER_ADDRESS` and `GKLOG_CPU_PROFILE_ENABLED=true`. These settings do not control required audit recording.

## Read design and operation guides

- [docs/plans/](docs/plans/) contains proposed designs, including audit subsystem
  horizontal scale and the Kafka cutover.
- [docs/runbooks/](docs/runbooks/) contains recovery and operational procedures.
- [Run search acceptance tests](docs/runbooks/search.md) provides native runner
  setup, process and cluster checks, and fixture cleanup instructions.
- Read [keys.go](internal/adapters/foundationdb/keys.go) for FDB keys,
  [migrations/](migrations/) for SQL schema,
  [config.go](internal/config/config.go) for configuration, and
  [docker-compose.yml](docker-compose.yml) for the stack.
