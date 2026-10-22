package observability

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrTelemetryExport = errors.New("telemetry export unavailable; observations may be incomplete")

type observedTraceExport struct {
	sdktrace.SpanExporter
	failed *atomic.Bool
}

func (e observedTraceExport) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	if err != nil {
		e.failed.Store(true)
	}
	return err
}

type observedMetricExport struct {
	sdkmetric.Exporter
	failed *atomic.Bool
}

func (e observedMetricExport) Export(ctx context.Context, metrics *metricdata.ResourceMetrics) error {
	err := e.Exporter.Export(ctx, metrics)
	if err != nil {
		e.failed.Store(true)
	}
	return err
}
func localCollector(endpoint string) (string, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") {
		return "", errors.New("local OTLP base URL required without credentials/query/path")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", errors.New("only explicitly configured loopback OTLP collectors are allowed")
	}
	return strings.TrimRight(endpoint, "/"), nil
}

// ConfigureTelemetry preserves the local file path and adds optional loopback
// OTLP traces and metrics. Network export is never silently enabled from env.
func ConfigureTelemetry(file, endpoint, service string) (func() error, error) {
	if endpoint == "" {
		return ConfigureTraceFile(file, service)
	}
	base, e := localCollector(endpoint)
	if e != nil {
		return nil, e
	}
	if service == "" {
		return nil, errors.New("service required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	traceExport, e := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(base+"/v1/traces"), otlptracehttp.WithHeaders(map[string]string{}), otlptracehttp.WithTimeout(2*time.Second), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: true, InitialInterval: 100 * time.Millisecond, MaxInterval: 300 * time.Millisecond, MaxElapsedTime: time.Second}))
	if e != nil {
		return nil, errors.New("trace exporter configuration rejected")
	}
	metricExport, e := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(base+"/v1/metrics"), otlpmetrichttp.WithHeaders(map[string]string{}), otlpmetrichttp.WithTimeout(2*time.Second), otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: true, InitialInterval: 100 * time.Millisecond, MaxInterval: 300 * time.Millisecond, MaxElapsedTime: time.Second}))
	if e != nil {
		_ = traceExport.Shutdown(ctx)
		return nil, errors.New("metric exporter configuration rejected")
	}
	var exportFailed atomic.Bool
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(failure error) {
		exportFailed.Store(true)
		category := "export"
		if errors.Is(failure, context.Canceled) {
			category = "canceled"
		}
		if errors.Is(failure, context.DeadlineExceeded) {
			category = "timeout"
		}
		log.Printf("telemetry export failed category=%s type=%T; observations may be incomplete", category, failure)
	}))
	res := resource.NewSchemaless(attribute.String("service.name", service))
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())), sdktrace.WithBatcher(observedTraceExport{SpanExporter: traceExport, failed: &exportFailed}, sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(2*time.Second))}
	var output *os.File
	if file != "" {
		output, e = os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			_ = traceExport.Shutdown(ctx)
			_ = metricExport.Shutdown(ctx)
			return nil, errors.New("trace output must be a new private file")
		}
		options = append(options, sdktrace.WithBatcher(&jsonExporter{writer: output}))
	}
	traces := sdktrace.NewTracerProvider(options...)
	metrics := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(observedMetricExport{Exporter: metricExport, failed: &exportFailed}, sdkmetric.WithInterval(time.Second), sdkmetric.WithTimeout(2*time.Second))))
	otel.SetTracerProvider(traces)
	otel.SetMeterProvider(metrics)
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			result = errors.Join(traces.Shutdown(ctx), metrics.Shutdown(ctx))
			if result != nil || exportFailed.Load() {
				result = ErrTelemetryExport
			}
			if output != nil {
				result = errors.Join(result, output.Close())
			}
		})
		return result
	}, nil
}
