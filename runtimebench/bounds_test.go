package runtimebench

import (
	"math"
	"testing"
	"time"
)

// These cases cover partial-worker waves and pre-arithmetic rejection rather
// than allowing a fast average estimate to hide a much slower final batch.
func TestSyntheticWorkloadDurationBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		config Config
		wantOK bool
	}{
		{"ninety-second callback", Config{Tasks: 1, Workers: 1, Delay: 90 * time.Second}, true},
		{"delay cap", Config{Tasks: 1, Workers: 1, Delay: 90*time.Second + time.Nanosecond}, false},
		{"exact final-wave limit", Config{Tasks: 3, Workers: 2, Delay: 60 * time.Second}, true},
		{"partial-wave exceeds limit", Config{Tasks: 3, Workers: 2, Delay: 60*time.Second + time.Nanosecond}, false},
		{"idle workers do not shorten callback", Config{Tasks: 1, Workers: 64, Delay: 90 * time.Second}, true},
		{"serial callbacks exceed drain limit", Config{Tasks: 2, Workers: 1, Delay: 90 * time.Second}, false},
		{"arrival exact limit", Config{Tasks: 120, Workers: 64, Delay: time.Millisecond, ArrivalEvery: time.Second}, true},
		{"arrival exceeds limit", Config{Tasks: 121, Workers: 64, Delay: time.Millisecond, ArrivalEvery: time.Second}, false},
		{"overflow-sized task count", Config{Tasks: math.MaxInt, Workers: 64, Delay: time.Millisecond, ArrivalEvery: time.Second}, false},
		{"overflow-sized worker count", Config{Tasks: 1, Workers: math.MaxInt, Delay: time.Millisecond}, false},
		{"overflow-sized delay", Config{Tasks: 10000, Workers: 64, Delay: time.Duration(math.MaxInt64)}, false},
		{"overflow-sized arrival", Config{Tasks: 10000, Workers: 64, Delay: time.Millisecond, ArrivalEvery: time.Duration(math.MaxInt64)}, false},
		{"negative arrival", Config{Tasks: 1, Workers: 1, Delay: time.Millisecond, ArrivalEvery: -1}, false},
		{"go-test file-work cap retained", Config{Tasks: 129, Workers: 8, Delay: time.Millisecond, Workload: "go-test"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeConfig(tc.config)
			if (err == nil) != tc.wantOK {
				t.Fatalf("accepted=%v, want=%v", err == nil, tc.wantOK)
			}
			if err == nil && tc.config.Workload == "" && got.Workload != "fixed-delay" {
				t.Fatal("default synthetic workload was not retained")
			}
		})
	}
}

func TestSlowCallbackTimeoutRetainsHeartbeatWindow(t *testing.T) {
	for _, tc := range []struct {
		config Config
		want   time.Duration
	}{
		{Config{Workload: "fixed-delay", Delay: time.Second}, 35 * time.Second},
		{Config{Workload: "fixed-delay", Delay: 35 * time.Second}, 40 * time.Second},
		{Config{Workload: "fixed-delay", Delay: 90 * time.Second}, 95 * time.Second},
		{Config{Workload: "go-test", Delay: 90 * time.Second}, 35 * time.Second},
	} {
		if got := callbackTimeout(tc.config); got != tc.want {
			t.Fatalf("timeout=%s, want=%s", got, tc.want)
		}
	}
}
