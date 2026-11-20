package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"

	"go.opentelemetry.io/otel"
	oteltrace "go.opentelemetry.io/otel/trace"
	control "goodkind.io/gklog/telemetry"
	"goodkind.io/gklog/telemetry/pyroscope"
	"goodkind.io/gklog/trace"
)

const telemetryService = "tack-server"

type multiCloser struct {
	closers []io.Closer
}

func (m multiCloser) Close() error {
	var closeErr error

	for _, closer := range slices.Backward(m.closers) {
		if closer == nil {
			continue
		}

		if err := closer.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}

	return closeErr
}

func setupError(operation string, err error, closer io.Closer) error {
	if closer != nil {
		if closeErr := closer.Close(); closeErr != nil {
			slog.Error("telemetry.cleanup", slog.Any("err", closeErr))
			err = errors.Join(err, closeErr)
		}
	}

	return fmt.Errorf("%s: %w", operation, err)
}

// ServerTelemetry owns the diagnostic controls for one running HTTP server.
type ServerTelemetry struct {
	Logging      *control.LogControl
	Tracing      *control.TraceControl
	Profiling    *control.ProfileControl
	MetricExport *control.MetricControl
	instanceID   string
	cleanup      multiCloser
}

// ServerStatus reports the settings this server currently applies.
type ServerStatus struct {
	InstanceID   string
	Logging      control.LogStatus
	Tracing      control.TraceStatus
	Profiling    control.BackendControlStatus
	MetricExport control.BackendControlStatus
}

// Status reads the local logging, sampling, profiling and export settings.
//
// Each control reports its applied state for this instance.
//
// Example:
//
//	status := server.Status()
func (server *ServerTelemetry) Status() ServerStatus {
	return ServerStatus{
		InstanceID:   server.instanceID,
		Logging:      server.Logging.Status(),
		Tracing:      server.Tracing.Status(),
		Profiling:    server.Profiling.Status(),
		MetricExport: server.MetricExport.Status(),
	}
}

// Close stops export and profiling and closes the server's log files.
//
// Close returns any errors from the telemetry providers and sinks.
//
// Example:
//
//	err := server.Close()
func (server *ServerTelemetry) Close() error {
	return server.cleanup.Close()
}

// SetupServer configures diagnostics for the lifetime of one HTTP server.
//
// GKLOG environment variables supply the startup settings. LOG_LEVEL supplies
// the initial log level when GKLOG_LOG_LEVEL is absent.
//
// Example:
//
//	server, err := SetupServer(logConfig, profileURL, metricEndpoint)
func SetupServer(cfg LogConfig, profileAddress, metricEndpoint string) (*ServerTelemetry, error) {
	logSettings, err := control.LogSettingsFromEnv()
	if err != nil {
		return nil, setupError("read diagnostic log settings", err, nil)
	}

	if _, set := os.LookupEnv("GKLOG_LOG_LEVEL"); !set && cfg.Level != "" {
		logSettings.Level = parseLogLevel(cfg.Level)
	}

	traceSettings, err := control.TraceSettingsFromEnv()
	if err != nil {
		return nil, setupError("read trace settings", err, nil)
	}

	logs, err := control.NewLogControl(logSettings)
	if err != nil {
		return nil, setupError("configure diagnostic logging", err, nil)
	}

	traces, err := control.NewTraceControl(traceSettings)
	if err != nil {
		return nil, setupError("configure trace sampling", err, nil)
	}

	logger, logCloser, err := buildLogger(cfg, logs)
	if err != nil {
		return nil, err
	}

	res, instance := serverResource()
	logger = logger.With(
		slog.String("service.name", telemetryService),
		slog.String(instanceAttribute, instance),
	)
	server := &ServerTelemetry{
		Logging:      logs,
		Tracing:      traces,
		Profiling:    nil,
		MetricExport: nil,
		instanceID:   instance,
		cleanup:      multiCloser{closers: []io.Closer{logCloser, logs, traces}},
	}
	ctx := context.Background()
	server.Profiling, err = setupProfiling(ctx, profileAddress, instance, logger)
	if err != nil {
		return nil, setupError("set up CPU profiling", err, server)
	}

	server.cleanup.closers = append(server.cleanup.closers, server.Profiling)
	if metricEndpoint == "" {
		metricEndpoint = cfg.OTELEndpoint
	}

	metrics, meter, err := setupMetrics(ctx, metricEndpoint, res)
	if err != nil {
		return nil, setupError("set up metric export", err, server)
	}

	server.MetricExport = metrics
	server.cleanup.closers = append(server.cleanup.closers, metrics)
	options := traceOptions(cfg.OTELEndpoint)
	options.Sampler = instanceSampler{Sampler: traces.Sampler(), instanceID: instance}
	if profileAddress != "" {
		options.WrapTracerProvider = func(provider oteltrace.TracerProvider) oteltrace.TracerProvider {
			return pyroscope.WrapTracerProvider(provider)
		}
	}

	traceCloser, err := trace.Setup(options)
	if err != nil {
		return nil, setupError("set up tracing", err, server)
	}

	server.cleanup.closers = append(server.cleanup.closers, traceCloser)
	if meter != nil {
		otel.SetMeterProvider(meter)
	}

	slog.SetDefault(logger)
	logger.Info("telemetry.configured", slog.Any("settings", server.Status()))

	return server, nil
}

func traceOptions(endpoint string) trace.Options {
	return trace.Options{
		ServiceName:         telemetryService,
		ServiceNamespace:    "tack",
		InstrumentationName: tackInstrumentationName,
		Endpoint:            endpoint,
	}
}
