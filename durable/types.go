package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Status string

const (
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
)

var (
	ErrNotFound = errors.New(
		"agent run not found",
	)

	ErrExists = errors.New(
		"agent run already exists",
	)

	ErrConflict = errors.New(
		"agent checkpoint revision conflict",
	)

	ErrTurnLimit = errors.New(
		"agent model turn limit reached",
	)

	ErrCallLimit = errors.New(
		"agent tool call limit reached",
	)
)

// Snapshot 表示某个 Agent Run 的完整检查点。
type Snapshot struct {
	RunID    string
	Repo     string
	Provider string
	Goal     string

	Status Status

	Revision int64
	Turns    int
	Calls    int

	History []json.RawMessage

	Final string
}

func (s Snapshot) Validate() error {
	if strings.TrimSpace(s.RunID) == "" ||
		strings.TrimSpace(s.Repo) == "" ||
		strings.TrimSpace(s.Goal) == "" ||
		strings.TrimSpace(s.Provider) == "" {

		return errors.New(
			"run_id, repo, provider, goal required",
		)
	}

	if s.Revision < 0 ||
		s.Turns < 0 ||
		s.Calls < 0 {

		return errors.New(
			"negative checkpoint counters",
		)
	}

	switch s.Status {
	case Running, Succeeded, Failed:
	default:
		return fmt.Errorf(
			"invalid status %q",
			s.Status,
		)
	}

	if len(s.History) == 0 {
		return errors.New(
			"history must contain user goal",
		)
	}

	for _, item := range s.History {
		if !json.Valid(item) {
			return errors.New(
				"history contains invalid JSON",
			)
		}
	}

	if s.Status == Succeeded &&
		strings.TrimSpace(s.Final) == "" {

		return errors.New(
			"succeeded run must have final text",
		)
	}

	return nil
}

// Store 是 Durable Agent 的持久化接口。
type Store interface {
	// 创建 Run 和初始 Checkpoint。
	Create(
		ctx context.Context,
		s Snapshot,
	) error

	// 加载当前 Run 的最新已提交 Checkpoint。
	Load(
		ctx context.Context,
		runID string,
	) (Snapshot, error)

	// expected 表示调用者读取到的旧 Revision。
	// 新 Snapshot 的 Revision 必须为 expected+1。
	Save(
		ctx context.Context,
		s Snapshot,
		expected int64,
	) error
}
