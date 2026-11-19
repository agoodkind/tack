package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/grafana/pyroscope-go/upstream/remote"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	control "goodkind.io/gklog/telemetry"
	"goodkind.io/gklog/telemetry/otlpmetrics"
	"goodkind.io/gklog/telemetry/pyroscope"
)

const instanceAttribute = "service.instance.id"

var errMetricEndpoint = errors.New(
	"set the OTLP metric endpoint to an HTTP or HTTPS collector URL or host:port",
)

func serverResource() (*resource.Resource, string) {
	base := resource.Default()
	value, _ := base.Set().Value(instanceAttribute)
	instance := value.AsString()
	if instance == "" {
		instance = uuid.NewString()
	}

	attrs := append(
		base.Attributes(),
		attribute.String("service.name", telemetryService),
		attribute.String("service.namespace", "tack"),
		attribute.String(instanceAttribute, instance),
	)

	return resource.NewWithAttributes(base.SchemaURL(), attrs...), instance
}

// gklog builds its trace resource from resource.Default. Put the instance
// ID on sampled spans as well when the process generates its own ID.
type instanceSampler struct {
	sdktrace.Sampler

	instanceID string
}

func (sampler instanceSampler) ShouldSample(
	params sdktrace.SamplingParameters,
) sdktrace.SamplingResult {
	result := sampler.Sampler.ShouldSample(params)
	if result.Decision == sdktrace.RecordAndSample {
		result.Attributes = append(
			result.Attributes,
			attribute.String(instanceAttribute, sampler.instanceID),
		)
	}

	return result
}

func setupMetrics(
	ctx context.Context,
	endpoint string,
	res *resource.Resource,
) (*control.MetricControl, *sdkmetric.MeterProvider, error) {
	enabled, err := control.MetricExportEnabledFromEnv()
	if err != nil {
		return nil, nil, setupError("read metric export setting", err, nil)
	}

	if endpoint == "" {
		metrics, err := control.NewMetricControl(ctx, nil, enabled)
		if err != nil {
			return nil, nil, setupError(
				"set OTEL_EXPORTER_OTLP_METRICS_ENDPOINT to enable metric export",
				err,
				nil,
			)
		}

		return metrics, nil, nil
	}

	options, err := metricExporterOptions(endpoint)
	if err != nil {
		return nil, nil, err
	}

	backend, err := otlpmetrics.New(
		ctx,
		otlpmetrics.Options{Resource: res, ExporterOptions: options},
	)
	if err != nil {
		return nil, nil, setupError("configure OTLP metric exporter", err, nil)
	}

	metrics, err := control.NewMetricControl(ctx, backend, enabled)
	if err != nil {
		return nil, nil, setupError("enable OTLP metric export", err, backend)
	}

	return metrics, backend.MeterProvider(), nil
}

func metricExporterOptions(endpoint string) ([]otlpmetricgrpc.Option, error) {
	if strings.Contains(endpoint, "://") {
		address, err := url.Parse(endpoint)
		if err != nil || address.Hostname() == "" ||
			(address.Scheme != "http" && address.Scheme != "https") {
			return nil, errMetricEndpoint
		}

		return []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpointURL(endpoint)}, nil
	}

	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return nil, errMetricEndpoint
	}

	return []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(endpoint)}, nil
}

func setupProfiling(
	ctx context.Context,
	endpoint string,
	instance string,
	logger *slog.Logger,
) (*control.ProfileControl, error) {
	enabled, err := control.ProfileEnabledFromEnv()
	if err != nil {
		return nil, setupError("read CPU profiling setting", err, nil)
	}

	if endpoint == "" {
		profiler, err := control.NewProfileControl(ctx, nil, enabled)
		if err != nil {
			return nil, setupError("set PYROSCOPE_SERVER_ADDRESS to enable CPU profiling", err, nil)
		}

		return profiler, nil
	}

	backend, err := pyroscope.New(pyroscope.Options{
		ApplicationName: telemetryService,
		Tags: map[string]string{
			"service_name":        telemetryService,
			"service_instance_id": instance,
		},
		Remote: remote.Config{Address: endpoint, Logger: profileLogger(logger)},
	})
	if err != nil {
		return nil, setupError("configure Pyroscope", err, nil)
	}

	profiler, err := control.NewProfileControl(ctx, backend, enabled)
	if err != nil {
		return nil, setupError("start CPU profiling", err, backend)
	}

	return profiler, nil
}

func profileLogger(logger *slog.Logger) *logrus.Logger {
	profiler := logrus.New()
	profiler.SetOutput(slog.NewLogLogger(logger.Handler(), slog.LevelError).Writer())
	profiler.SetLevel(logrus.ErrorLevel)

	return profiler
}
