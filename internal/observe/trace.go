package observe

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// tracerName is the instrumentation scope for spans emitted by the server.
const tracerName = "github.com/hanshuebner/herold"

// NewTracer builds a trace.Tracer backed by an OTLP/HTTP exporter
// (REQ-OPS-100..103). When endpoint is empty the returned tracer is a no-op
// and the shutdown function returns nil. Otherwise the exporter is wired via
// a BatchSpanProcessor to a TracerProvider; shutdown flushes and stops it,
// honouring the caller's context deadline.
func NewTracer(ctx context.Context, endpoint string) (trace.Tracer, func(context.Context) error, error) {
	if endpoint == "" {
		tp := noop.NewTracerProvider()
		return tp.Tracer(tracerName), func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("observe: otlp exporter: %w", err)
	}
	bsp := sdktrace.NewBatchSpanProcessor(exp)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(bsp))
	shutdown := func(ctx context.Context) error {
		return tp.Shutdown(ctx)
	}
	return tp.Tracer(tracerName), shutdown, nil
}
