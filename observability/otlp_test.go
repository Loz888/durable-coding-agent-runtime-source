package observability

import (
	"bytes"
	"context"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace/noop"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func resetTelemetry(t *testing.T) {
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
	})
}
func TestOTLPRealProtocolAndRedaction(t *testing.T) {
	resetTelemetry(t)
	var traces, metrics atomic.Int64
	var leaked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Authorization") != "" || bytes.Contains(raw, []byte("PRIVATE-ERROR-MARKER")) {
			leaked.Store(true)
		}
		switch r.URL.Path {
		case "/v1/traces":
			var request tracepb.ExportTraceServiceRequest
			if proto.Unmarshal(raw, &request) != nil || len(request.ResourceSpans) == 0 {
				t.Error("not actual OTLP trace protocol")
				w.WriteHeader(400)
				return
			}
			traces.Add(1)
		case "/v1/metrics":
			var request metricpb.ExportMetricsServiceRequest
			if proto.Unmarshal(raw, &request) != nil || len(request.ResourceMetrics) == 0 {
				t.Error("not actual OTLP metric protocol")
				w.WriteHeader(400)
				return
			}
			for _, resource := range request.ResourceMetrics {
				for _, scope := range resource.ScopeMetrics {
					for _, m := range scope.Metrics {
						if sum := m.GetSum(); sum != nil {
							for _, point := range sum.DataPoints {
								for _, a := range point.Attributes {
									if a.Key != "operation" {
										t.Error("high-cardinality/private metric label", a.Key)
									}
								}
							}
						}
					}
				}
			}
			metrics.Add(1)
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=PRIVATE-ERROR-MARKER")
	close, e := ConfigureTelemetry("", server.URL, "test-otlp")
	if e != nil {
		t.Fatal(e)
	}
	_, span := StartOperation(context.Background(), "test.failure", attribute.String("run.id", "fixture-only"))
	FinishOperation(span, errors.New("PRIVATE-ERROR-MARKER"))
	FinishOperation(span, nil)
	if e = close(); e != nil {
		t.Fatal(e)
	}
	if traces.Load() == 0 || metrics.Load() == 0 || leaked.Load() {
		t.Fatal("missing export or sensitive data leaked", traces.Load(), metrics.Load(), leaked.Load())
	}
}
func TestOTLPUnavailableBoundedAndExplicitOptIn(t *testing.T) {
	resetTelemetry(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	for _, endpoint := range []string{"https://example.com", "http://user:password@127.0.0.1:4318", "http://127.0.0.1:4318/?token=x"} {
		if _, e := ConfigureTelemetry("", endpoint, "reject"); e == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	close, e := ConfigureTelemetry("", "", "disabled")
	if e != nil {
		t.Fatal(e)
	}
	if e = close(); e != nil {
		t.Fatal(e)
	}
	if requests.Load() != 0 {
		t.Fatal("ambient env enabled network export")
	}
	close, e = ConfigureTelemetry("", server.URL, "unavailable")
	if e != nil {
		t.Fatal(e)
	}
	_, span := StartOperation(context.Background(), "test.unavailable")
	FinishOperation(span, nil)
	started := time.Now()
	e = close()
	if !errors.Is(e, ErrTelemetryExport) || time.Since(started) > 6*time.Second || strings.Contains(e.Error(), "body:") {
		t.Fatal("unavailable exporter not reported within bound", e)
	}
}

func TestOTLPRecoversTransientExporterFailure(t *testing.T) {
	var traces, metrics atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var count *atomic.Int32
		if r.URL.Path == "/v1/traces" {
			count = &traces
		} else {
			count = &metrics
		}
		if count.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	close, err := ConfigureTelemetry("", server.URL, "retry-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := StartOperation(context.Background(), "retry.operation")
	_ = ctx
	FinishOperation(span, nil)
	if err = close(); err != nil {
		t.Fatal(err)
	}
	if traces.Load() < 2 || metrics.Load() < 2 {
		t.Fatalf("missing retry: traces=%d metrics=%d", traces.Load(), metrics.Load())
	}
}
