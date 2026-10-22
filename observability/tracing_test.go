package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace/noop"
	"strings"
	"testing"
)

func TestJSONTracingPreservesContextAndOmitsErrorPayload(t *testing.T) {
	var output bytes.Buffer
	provider, e := NewJSONTracing(&output, "fixture-service")
	if e != nil {
		t.Fatal(e)
	}
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(noop.NewTracerProvider())
	defer provider.Shutdown(context.Background())
	ctx, submit := StartOperation(context.Background(), "submit", attribute.String("run.id", "fixture"))
	parent, state := TraceCarrier(ctx)
	if parent == "" {
		t.Fatal("missing trace carrier")
	}
	resumed := RestoreTrace(context.Background(), parent, state)
	_, worker := StartOperation(resumed, "worker")
	FinishOperation(worker, errors.New("sensitive-source-or-key-must-not-be-exported"))
	FinishOperation(submit, nil)
	if e = provider.ForceFlush(context.Background()); e != nil {
		t.Fatal(e)
	}
	decoder := json.NewDecoder(&output)
	var a, b JSONSpan
	if e = decoder.Decode(&a); e != nil {
		t.Fatal(e)
	}
	if e = decoder.Decode(&b); e != nil {
		t.Fatal(e)
	}
	if a.Service != "fixture-service" || b.Service != "fixture-service" || a.TraceID != b.TraceID || a.ParentSpanID != b.SpanID || a.Status != "Error" || b.Name != "submit" {
		t.Fatalf("unlinked spans %+v %+v", a, b)
	}
	raw, _ := json.Marshal([]JSONSpan{a, b})
	if strings.Contains(string(raw), "sensitive-source") {
		t.Fatal("raw error payload exported")
	}
}
