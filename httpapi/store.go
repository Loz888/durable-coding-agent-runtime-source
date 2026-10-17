package httpapi

import (
	"fmt"
	"sync"

	"runtime-app/runtimecore"
	"runtime-app/task"
)

// TaskRecord 是 HTTP API 对外提供的任务视图。
type TaskRecord struct {
	TaskID int `json:"task_id"`

	Name     string `json:"name"`
	ToolName string `json:"tool_name"`

	State string `json:"state"`

	Attempts int `json:"attempts"`
	Retries  int `json:"retries"`

	Stdout string `json:"stdout,omitempty"`

	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`

	ErrorKind string `json:"error_kind,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ResultStore 保存可查询的任务记录。
type ResultStore struct {
	mu sync.RWMutex

	records map[int]TaskRecord
}

func NewResultStore() *ResultStore {
	return &ResultStore{
		records: make(map[int]TaskRecord),
	}
}

// Create 先保留 TaskID，避免并发重复提交。
func (s *ResultStore) Create(
	t task.Task,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.records[t.ID]; exists {
		return false
	}

	s.records[t.ID] = TaskRecord{
		TaskID:   t.ID,
		Name:     t.Name,
		ToolName: t.ToolName,
		State:    "pending",
	}

	return true
}

// Delete 用于 Submit 失败后的回滚。
func (s *ResultStore) Delete(
	taskID int,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.records, taskID)
}

// Complete 保存最终 Result。
func (s *ResultStore) Complete(
	result runtimecore.Result,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[result.TaskID]

	if !exists {
		return fmt.Errorf(
			"result for unknown task %d",
			result.TaskID,
		)
	}

	record.State = fmt.Sprint(result.State)

	record.Attempts = len(result.Attempts)

	if record.Attempts > 1 {
		record.Retries = record.Attempts - 1
	}

	record.Stdout = result.Stdout

	record.ErrorKind = fmt.Sprint(
		result.ErrorKind,
	)

	if result.Err != nil {
		record.Error = result.Err.Error()
	}

	s.records[result.TaskID] = record

	return nil
}

// Get 返回一个独立的记录副本。
func (s *ResultStore) Get(
	taskID int,
) (TaskRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, exists := s.records[taskID]

	return record, exists
}
