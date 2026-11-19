# Server telemetry

Each Tack server counts the HTTP requests it receives. A request log shows
the response status and elapsed time for one request. A sampled trace shows
how long individual operations took. A CPU profile shows which functions
used processor time while profiling was active.

## Logs and traces

Tack returns a request ID in the `X-Request-ID` response header. That value
matches the `request_id` in the request log. The log also includes a
`trace_id` for the whole trace and a `span_id` for Tack’s part of the request.

Tack reads its diagnostic settings at startup. `GKLOG_LOG_ENABLED` controls
diagnostic logging, and `GKLOG_LOG_LEVEL` sets its minimum level.
`GKLOG_TRACE_ENABLED` and `GKLOG_TRACE_SAMPLE_RATIO` determine which new
traces Tack samples. When a request continues an existing trace, Tack follows
the parent’s sampling decision. Tack exports sampled traces when
`OTEL_EXPORTER_OTLP_ENDPOINT` specifies a collector.

## Request metrics

`GET /debug/vars` reports `tack_http_requests_total` for the server that
answered. The count starts at zero when the process starts and includes the
request to read it.

With `GKLOG_METRIC_EXPORT_ENABLED=true`, Tack also exports the count as
`tack.http.requests` to an OpenTelemetry collector over gRPC.
`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` selects the collector for metrics.
When that setting is empty, Tack uses `OTEL_EXPORTER_OTLP_ENDPOINT`.
An exported count can include an exemplar, a measurement with the trace and
span IDs of one sampled request. Those IDs identify the matching request log.

## CPU profiles

With `GKLOG_CPU_PROFILE_ENABLED=true`, Tack sends CPU samples to the
Pyroscope server at `PYROSCOPE_SERVER_ADDRESS`. When profiling and tracing
are both active, profile labels associate CPU samples with a sampled request.
A slow request can have few CPU samples if it spent most of its time waiting.
