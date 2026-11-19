# Server telemetry

When an HTTP request takes longer than expected, its log shows the response
status and total elapsed time. A trace breaks that time down by operation.
A CPU profile shows which functions used processor time. Each Tack server
also counts requests, including those it does not sample for tracing.

## A single request

Tack returns the request ID in the `X-Request-ID` response header. The same
value appears as `request_id` in the request log. For a sampled request, the
log’s `trace_id` identifies the full trace. Its `span_id` identifies the
server span, which measures how long Tack spent handling the request.

A long trace can have few CPU samples. For example, time spent waiting for a
database response contributes to elapsed time. The CPU profiler samples
functions while they execute. When tracing and profiling are both active,
Pyroscope uses profile labels to associate samples with the request.

## Request counts

Each server reports its own total as `tack_http_requests_total` at
`GET /debug/vars`. The count starts at zero each time the process starts.
Reading the endpoint counts as a request.

With metric export enabled, Tack sends the count to an OpenTelemetry collector
over gRPC as `tack.http.requests`. The count can include an *exemplar*, an
example measurement from one sampled request. Its trace and span IDs identify
the corresponding trace and log entry.

## Startup settings

Each Tack server reads these environment variables when it starts.

| Signal | Settings |
| --- | --- |
| Diagnostic logs | `GKLOG_LOG_ENABLED` controls logging. `GKLOG_LOG_LEVEL` sets the minimum level. |
| Traces | `GKLOG_TRACE_ENABLED` controls sampling for new traces. `GKLOG_TRACE_SAMPLE_RATIO` sets the fraction to sample. Requests that continue a trace follow the parent’s sampling decision. |
| CPU profiles | `GKLOG_CPU_PROFILE_ENABLED=true` starts CPU profiling. `PYROSCOPE_SERVER_ADDRESS` selects the Pyroscope server that receives the samples. |
| Request metrics | `GKLOG_METRIC_EXPORT_ENABLED=true` enables export. The local request count remains available at either setting. |

`OTEL_EXPORTER_OTLP_ENDPOINT` selects the collector for sampled traces and
exported metrics. `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` overrides that
destination for metrics alone.
