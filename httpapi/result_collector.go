package httpapi

import (
	"context"
	"runtime-app/persistence"
	"runtime-app/runtimecore"
)

// ResultCollector writes terminal runtime results through the storage interface.
type ResultCollector struct {
	store persistence.TaskStore
}

func NewResultCollector(store persistence.TaskStore) *ResultCollector {
	return &ResultCollector{store: store}
}

func (c *ResultCollector) Record(ctx context.Context, result runtimecore.Result) error {
	record, err := c.store.Get(ctx, int64(result.TaskID))
	if err != nil {
		return err
	}
	record.State = string(result.State)
	record.Stdout = result.Stdout
	record.Stderr = result.Stderr
	record.ExitCode = result.ExitCode
	record.ErrorKind = string(result.ErrorKind)
	record.Error = ""
	if result.Err != nil {
		record.Error = result.Err.Error()
	}
	record.Attempts = len(result.Attempts)
	record.Retries = 0
	if record.Attempts > 1 {
		record.Retries = record.Attempts - 1
	}
	return c.store.Update(ctx, record)
}

// resultStoreAdapter keeps the existing HTTP memory store usable as a TaskStore.
type resultStoreAdapter struct {
	store *ResultStore
}

var _ persistence.TaskStore = (*resultStoreAdapter)(nil)

func (a *resultStoreAdapter) Create(ctx context.Context, record persistence.TaskRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	id := int(record.ID)
	if _, exists := a.store.records[id]; exists {
		return persistence.ErrDuplicateTask
	}
	a.store.records[id] = httpRecord(record)
	return nil
}

func (a *resultStoreAdapter) Get(ctx context.Context, id int64) (persistence.TaskRecord, error) {
	if err := ctx.Err(); err != nil {
		return persistence.TaskRecord{}, err
	}
	record, exists := a.store.Get(int(id))
	if !exists {
		return persistence.TaskRecord{}, persistence.ErrTaskNotFound
	}
	return persistence.TaskRecord{
		ID: int64(record.TaskID), Name: record.Name, ToolName: record.ToolName,
		State: record.State, ErrorKind: record.ErrorKind, Error: record.Error,
		Stdout: record.Stdout, Stderr: record.Stderr, ExitCode: record.ExitCode,
		Attempts: record.Attempts, Retries: record.Retries,
	}, nil
}

func (a *resultStoreAdapter) Update(ctx context.Context, record persistence.TaskRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	id := int(record.ID)
	if _, exists := a.store.records[id]; !exists {
		return persistence.ErrTaskNotFound
	}
	a.store.records[id] = httpRecord(record)
	return nil
}

func httpRecord(record persistence.TaskRecord) TaskRecord {
	return TaskRecord{
		TaskID: int(record.ID), Name: record.Name, ToolName: record.ToolName,
		State: record.State, ErrorKind: record.ErrorKind, Error: record.Error,
		Stdout: record.Stdout, Stderr: record.Stderr, ExitCode: record.ExitCode,
		Attempts: record.Attempts, Retries: record.Retries,
	}
}

// Delete rolls back an unaccepted task in the memory backend.
func (a *resultStoreAdapter) Delete(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.store.Delete(int(id))
	return nil
}
