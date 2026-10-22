package main

import (
	"strings"
	"testing"
)

// Config parsing does not establish a database connection. These tests ensure
// the explicit override cannot strand the benchmark behind its own guard.
func TestBenchmarkPoolConfigPreservesDSNAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dsn   string
		limit int
		want  int32
	}{
		{"DSN preserved", "host=127.0.0.1 dbname=agent_runtime_day14_test pool_max_conns=20", 0, 20},
		{"low bounded override", "host=127.0.0.1 dbname=agent_runtime_day14_test pool_max_conns=20", 2, 2},
		{"upper bounded override", "host=127.0.0.1 dbname=agent_runtime_day14_test", 64, 64},
		{"guard would deadlock", "host=127.0.0.1 dbname=agent_runtime_day14_test pool_max_conns=1", 0, 0},
		{"explicit singleton", "host=127.0.0.1 dbname=agent_runtime_day14_test", 1, 0},
		{"negative override", "host=127.0.0.1 dbname=agent_runtime_day14_test", -1, 0},
		{"excessive override", "host=127.0.0.1 dbname=agent_runtime_day14_test", 65, 0},
		{"minimum conflicts with override", "host=127.0.0.1 dbname=agent_runtime_day14_test pool_min_conns=4", 2, 0},
		{"idle minimum conflicts with override", "host=127.0.0.1 dbname=agent_runtime_day14_test pool_min_idle_conns=4", 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := benchmarkPoolConfig(tc.dsn, tc.limit)
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid pool configuration accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxConns != tc.want {
				t.Fatalf("pool max=%d, want=%d", cfg.MaxConns, tc.want)
			}
		})
	}
}

func TestBenchmarkPoolParseErrorDoesNotExposeConfiguration(t *testing.T) {
	_, err := benchmarkPoolConfig("postgres://fixture:TEST_PASSWORD@127.0.0.1:invalid/test", 0)
	if err == nil {
		t.Fatal("invalid connection configuration accepted")
	}
	if strings.Contains(err.Error(), "TEST_PASSWORD") || strings.Contains(err.Error(), "postgres://") {
		t.Fatal("configuration leaked through parse error")
	}
}
