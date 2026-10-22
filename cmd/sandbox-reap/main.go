package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime-app/executor"
	"syscall"
	"time"
)

func run() error {
	root := flag.String("root", "", "canonical private sandbox root")
	image := flag.String("image", "", "pinned sandbox image policy")
	interval := flag.Duration("interval", 0, "zero runs once; otherwise supervise independently every 1s..1m")
	flag.Parse()
	if *interval < 0 || (*interval > 0 && (*interval < time.Second || *interval > time.Minute)) {
		return fmt.Errorf("bounded reaper interval required")
	}
	policy := executor.GoSandbox{Root: *root, Image: *image, Timeout: 30 * time.Second, MaxBytes: 64 << 20, MaxFiles: 10000}
	if e := policy.Validate(); e != nil {
		return e
	}
	ctx, done := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer done()
	for {
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, e := policy.ReapExpired(call)
		cancel()
		if e != nil {
			return e
		}
		if e = json.NewEncoder(os.Stdout).Encode(result); e != nil {
			return e
		}
		if *interval == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "sandbox reaper failed:", e)
		os.Exit(1)
	}
}
