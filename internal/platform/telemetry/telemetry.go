// Package telemetry configures OpenTelemetry traces, metrics, and logs exported over OTLP/HTTP
// (OpenObserve in deployed environments). It is configured by the standard OTEL_* variables.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const defaultServiceName = "ioe-backend"

// Telemetry owns the SDK providers. LogHandler is nil when telemetry is disabled.
type Telemetry struct {
	LogHandler slog.Handler
	shutdown   []func(context.Context) error
}

// Shutdown flushes and stops all providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(t.shutdown) - 1; i >= 0; i-- {
		errs = append(errs, t.shutdown[i](ctx))
	}
	return errors.Join(errs...)
}

// Setup installs W3C propagators and, when an OTLP endpoint is configured and
// OTEL_SDK_DISABLED is not "true", global tracer and meter providers plus a log handler.
func Setup(ctx context.Context, serviceVersion string) (*Telemetry, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t := &Telemetry{}
	if !enabled() {
		return t, nil
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", defaultServiceName),
			attribute.String("service.version", serviceVersion),
		),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}

	te, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(te), sdktrace.WithResource(res))
	t.shutdown = append(t.shutdown, tp.Shutdown)

	me, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me)), sdkmetric.WithResource(res))
	t.shutdown = append(t.shutdown, mp.Shutdown)

	le, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(le)), sdklog.WithResource(res))
	t.shutdown = append(t.shutdown, lp.Shutdown)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	t.LogHandler = otelslog.NewLogger(defaultServiceName, otelslog.WithLoggerProvider(lp)).Handler()
	return t, nil
}

func enabled() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false
	}
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}
