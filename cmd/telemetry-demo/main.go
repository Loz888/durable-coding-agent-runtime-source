package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime-app/observability"
	"time"
)

func run() error {
	endpoint := flag.String("otlp-endpoint", "", "required explicit loopback collector")
	output := flag.String("output", "", "required new private report")
	flag.Parse()
	if *endpoint == "" || *output == "" {
		return errors.New("loopback endpoint and new output required")
	}
	file, e := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return errors.New("new evidence file required")
	}
	defer file.Close()
	close, e := observability.ConfigureTelemetry(*output+".spans.jsonl", *endpoint, "runtime-observe-demo")
	if e != nil {
		return e
	}
	ctx, root := observability.StartOperation(context.Background(), "demo.execution")
	parent, state := observability.TraceCarrier(ctx)
	_, good := observability.StartOperation(ctx, "demo.verify")
	observability.FinishOperation(good, nil)
	// Prime a zero error counter before the controlled increase so Prometheus can
	// evaluate an actual time-window change instead of a startup counter value.
	time.Sleep(5 * time.Second)
	_, negative := observability.StartOperation(ctx, "demo.verify")
	observability.FinishOperation(negative, errors.New("controlled acceptance negative, not a production incident"))
	observability.FinishOperation(root, nil)
	time.Sleep(5 * time.Second)
	e = close()
	status := "PASS"
	if e != nil {
		status = "FAIL"
	}
	writeErr := json.NewEncoder(file).Encode(map[string]any{"status": status, "controlled_negative": true, "traceparent": parent, "tracestate": state, "model_calls": 0, "scope": "real local OTLP/Jaeger/Prometheus pipeline, intentionally emitted error for alert acceptance"})
	if writeErr != nil {
		return writeErr
	}
	if e != nil {
		return errors.New("telemetry export failed")
	}
	return nil

}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
