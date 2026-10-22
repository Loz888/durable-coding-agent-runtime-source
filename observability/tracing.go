package observability

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Tracing is opt-in. Only operation metadata is recorded: no prompts, source,
// repository paths, tool output, credentials or raw error messages.
type operationSpan struct {
	trace.Span
	started time.Time
	ctx     context.Context
	name    string
	once    sync.Once
}

func StartOperation(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	ctx, span := otel.Tracer("runtime-app/durable").Start(ctx, name, trace.WithAttributes(attrs...))
	errorsCounter, _ := otel.Meter("runtime-app/durable").Int64Counter("runtime.operation.errors")
	errorsCounter.Add(ctx, 0, metric.WithAttributes(attribute.String("operation", name)))
	return ctx, &operationSpan{Span: span, started: time.Now(), ctx: ctx, name: name}
}
func FinishOperation(span trace.Span, err error) {
	finish := func() {
		if err != nil {
			span.SetStatus(codes.Error, "operation failed")
		}
		if op, ok := span.(*operationSpan); ok {
			meter := otel.Meter("runtime-app/durable")
			duration, _ := meter.Float64Histogram("runtime.operation.duration", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120))
			count, _ := meter.Int64Counter("runtime.operation.count")
			failure, _ := meter.Int64Counter("runtime.operation.errors")
			attrs := metric.WithAttributes(attribute.String("operation", op.name))
			duration.Record(op.ctx, time.Since(op.started).Seconds(), attrs)
			count.Add(op.ctx, 1, attrs)
			if err != nil {
				failure.Add(op.ctx, 1, attrs)
			}
		}
		span.End()
	}
	if op, ok := span.(*operationSpan); ok {
		op.once.Do(finish)
	} else {
		finish()
	}
}

func TraceCarrier(ctx context.Context) (string, string) {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent"), carrier.Get("tracestate")
}

func RestoreTrace(ctx context.Context, parent, state string) context.Context {
	if parent == "" {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent, "tracestate": state})
}

type JSONSpan struct {
	Service      string         `json:"service"`
	Name         string         `json:"name"`
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Start        time.Time      `json:"start"`
	End          time.Time      `json:"end"`
	Status       string         `json:"status"`
	Attributes   map[string]any `json:"attributes"`
}

type jsonExporter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (e *jsonExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	encoder := json.NewEncoder(e.writer)
	for _, s := range spans {
		service := ""
		for _, a := range s.Resource().Attributes() {
			if string(a.Key) == "service.name" {
				service = a.Value.AsString()
			}
		}
		attrs := make(map[string]any)
		for _, a := range s.Attributes() {
			attrs[string(a.Key)] = a.Value.AsInterface()
		}
		parent := ""
		if s.Parent().IsValid() {
			parent = s.Parent().SpanID().String()
		}
		if err := encoder.Encode(JSONSpan{Service: service, Name: s.Name(), TraceID: s.SpanContext().TraceID().String(), SpanID: s.SpanContext().SpanID().String(), ParentSpanID: parent, Start: s.StartTime(), End: s.EndTime(), Status: s.Status().Code.String(), Attributes: attrs}); err != nil {
			return err
		}
	}
	return nil
}
func (*jsonExporter) Shutdown(context.Context) error { return nil }

// NewJSONTracing uses the real OpenTelemetry SDK with a local NDJSON exporter.
// It does not contact a collector or a paid observability service.
func NewJSONTracing(writer io.Writer, service string) (*sdktrace.TracerProvider, error) {
	if writer == nil || service == "" {
		return nil, errors.New("trace writer and service required")
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(&jsonExporter{writer: writer}),
	), nil
}

// ConfigureTraceFile never overwrites an existing evidence file. An empty path
// leaves the default no-op provider in place.
func ConfigureTraceFile(path, service string) (func() error, error) {
	if path == "" {
		return func() error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("trace output must be a new writable file")
	}
	provider, err := NewJSONTracing(file, service)
	if err != nil {
		file.Close()
		return nil, err
	}
	otel.SetTracerProvider(provider)
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result = provider.Shutdown(ctx)
			if closeErr := file.Close(); result == nil {
				result = closeErr
			}
		})
		return result
	}, nil
}
