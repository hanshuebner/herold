package observe

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

// OTLPLoggerConfig holds the parameters needed to construct an OTLP log
// provider (REQ-OPS-205). Mirrors the fields set for the trace exporter so
// both share the same [otlp] config block.
type OTLPLoggerConfig struct {
	// Endpoint is the host:port of the OTLP/HTTP collector. Empty means
	// OTLP log export is disabled (noop provider returned).
	Endpoint string
	// DeploymentEnvironment is the value of the deployment.environment
	// resource attribute. Defaults to "production" when empty.
	DeploymentEnvironment string
	// ServiceInstanceID is the service.instance.id resource attribute.
	// Defaults to os.Hostname() when empty.
	ServiceInstanceID string
}

// NewOTLPLogProvider builds an OTLP log provider backed by an HTTP exporter
// (REQ-OPS-205). When Endpoint is empty the returned provider is a noop and
// the shutdown function is a no-op. Otherwise the exporter is wired via a
// BatchProcessor; shutdown flushes and stops it, honouring the caller's
// context deadline.
//
// The resource attributes service.name, service.version, deployment.environment,
// and service.instance.id are NOT set here because service.name must be set
// per-event (herold-suite vs herold-admin). Use [LoggerForService] to obtain a
// logger scoped to a specific service name and build SHA.
func NewOTLPLogProvider(ctx context.Context, cfg OTLPLoggerConfig) (otellog.LoggerProvider, func(context.Context) error, error) {
	if cfg.Endpoint == "" {
		return noop.NewLoggerProvider(), func(context.Context) error { return nil }, nil
	}

	env := cfg.DeploymentEnvironment
	if env == "" {
		env = "production"
	}

	instanceID := cfg.ServiceInstanceID
	if instanceID == "" {
		if h, err := os.Hostname(); err == nil {
			instanceID = h
		}
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("deployment.environment", env),
			attribute.String("service.instance.id", instanceID),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("observe: otlp log resource: %w", err)
	}

	exp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpoint(cfg.Endpoint),
		otlploghttp.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("observe: otlp log exporter: %w", err)
	}

	bp := sdklog.NewBatchProcessor(exp)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(bp),
		sdklog.WithResource(res),
	)

	shutdown := func(ctx context.Context) error {
		return lp.Shutdown(ctx)
	}
	return lp, shutdown, nil
}

// LoggerForService returns an otellog.Logger scoped to the given service name
// and build SHA. The service.name and service.version are encoded in the
// instrumentation scope name per the OTLP semantic conventions for logs.
func LoggerForService(lp otellog.LoggerProvider, serviceName, buildSHA string) otellog.Logger {
	return lp.Logger(serviceName, otellog.WithInstrumentationVersion(buildSHA))
}
