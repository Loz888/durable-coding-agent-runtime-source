package httpapi

import (
	"context"
	"errors"
	"runtime-app/persistence"
	"runtime-app/runtimecore"
	"testing"
)

type collectorTestStore struct {
	record    persistence.TaskRecord
	getErr    error
	updateErr error
	updates   int
}

func (s *collectorTestStore) Create(context.Context, persistence.TaskRecord) error { return nil }
func (s *collectorTestStore) Get(context.Context, int64) (persistence.TaskRecord, error) {
	return s.record, s.getErr
}
func (s *collectorTestStore) Update(_ context.Context, r persistence.TaskRecord) error {
	s.updates++
	s.record = r
	return s.updateErr
}
func TestResultCollectorUsesTaskStore(t *testing.T) {
	store := &collectorTestStore{record: persistence.TaskRecord{ID: 42, Name: "job", ToolName: "uppercase"}}
	collector := NewResultCollector(store)
	result := runtimecore.Result{TaskID: 42, State: runtimecore.StateFailed,
		Stdout: "out", Stderr: "err", ExitCode: 2, Err: errors.New("failed"),
		Attempts: make([]runtimecore.Attempt, 3)}
	if err := collector.Record(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	r := store.record
	if store.updates != 1 || r.ID != 42 || r.Name != "job" || r.ToolName != "uppercase" ||
		r.State != "failed" || r.Stdout != "out" || r.Stderr != "err" ||
		r.ExitCode != 2 || r.Error != "failed" || r.Attempts != 3 || r.Retries != 2 {
		t.Fatalf("unexpected persisted record: %+v", r)
	}
}
func TestResultCollectorStorageErrors(t *testing.T) {
	sentinel := errors.New("storage failed")
	store := &collectorTestStore{getErr: sentinel}
	collector := NewResultCollector(store)
	if err := collector.Record(context.Background(), runtimecore.Result{}); !errors.Is(err, sentinel) {
		t.Fatalf("Get error = %v", err)
	}
	if store.updates != 0 {
		t.Fatal("Update called after failed Get")
	}
	store.getErr = nil
	store.updateErr = sentinel
	if err := collector.Record(context.Background(), runtimecore.Result{}); !errors.Is(err, sentinel) {
		t.Fatalf("Update error = %v", err)
	}
}

func (s *collectorTestStore) Delete(context.Context, int64) error { return nil }
