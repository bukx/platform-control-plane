package observability

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type TelemetryConfig struct {
	ServiceName       string
	OTLPEndpoint      string
	PrometheusEnabled bool
}

type Telemetry struct {
	MetricsHandler http.Handler
	Meter          metric.Meter
	shutdown       func(context.Context) error
}

func SetupTelemetry(ctx context.Context, cfg TelemetryConfig) (*Telemetry, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	var traceProvider *sdktrace.TracerProvider
	if cfg.OTLPEndpoint != "" {
		exporter, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}

		traceProvider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exporter),
			sdktrace.WithResource(res),
		)
	} else {
		traceProvider = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
		)
	}

	otel.SetTracerProvider(traceProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	meterOptions := []sdkmetric.Option{
		sdkmetric.WithResource(res),
	}

	telemetry := &Telemetry{
		shutdown: func(ctx context.Context) error {
			return traceProvider.Shutdown(ctx)
		},
	}

	if cfg.PrometheusEnabled {
		exporter, err := prometheus.New()
		if err != nil {
			return nil, fmt.Errorf("create prometheus exporter: %w", err)
		}
		meterOptions = append(meterOptions, sdkmetric.WithReader(exporter))
		telemetry.MetricsHandler = promhttp.Handler()
	}

	meterProvider := sdkmetric.NewMeterProvider(meterOptions...)
	otel.SetMeterProvider(meterProvider)
	telemetry.Meter = meterProvider.Meter(cfg.ServiceName)

	telemetry.shutdown = func(ctx context.Context) error {
		var firstErr error
		if err := meterProvider.Shutdown(ctx); err != nil {
			firstErr = err
		}
		if err := traceProvider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}

	return telemetry, nil
}

func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil || t.shutdown == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return t.shutdown(ctx)
}

func HTTPMiddleware(name string, next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, name)
}
