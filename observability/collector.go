package observability

import (
	"fmt"
	"sync"
	"time"

	"runtime-app/runtimecore"
)

type Snapshot struct {
	Completed int
	Succeeded int
	Failed    int
	Cancelled int

	Attempts int
	Retries  int

	TotalAttemptDuration time.Duration

	ErrorsByKind map[runtimecore.ErrorKind]int
}

type Collector struct {
	mu sync.Mutex

	seen map[int]struct{}

	completed int
	succeeded int
	failed    int
	cancelled int

	attempts int
	retries  int

	totalAttemptDuration time.Duration

	errorsByKind map[runtimecore.ErrorKind]int
}

func NewCollector() *Collector {
	return &Collector{
		seen: make(map[int]struct{}),

		errorsByKind: make(
			map[runtimecore.ErrorKind]int,
		),
	}
}

func (c *Collector) Record(
	result runtimecore.Result,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 不允许同一个逻辑 Task 重复计入结果。
	if _, exists := c.seen[result.TaskID]; exists {
		return fmt.Errorf(
			"duplicate result for task %d",
			result.TaskID,
		)
	}

	// 这里只接受终态 Result。
	switch result.State {
	case runtimecore.StateSucceeded:
		// valid

	case runtimecore.StateFailed:
		// valid

	case runtimecore.StateCancelled:
		// valid

	default:
		return fmt.Errorf(
			"non-terminal state for task %d: %s",
			result.TaskID,
			result.State,
		)
	}

	c.seen[result.TaskID] = struct{}{}

	c.completed++

	switch result.State {
	case runtimecore.StateSucceeded:
		c.succeeded++

	case runtimecore.StateFailed:
		c.failed++

	case runtimecore.StateCancelled:
		c.cancelled++
	}

	c.errorsByKind[result.ErrorKind]++

	n := len(result.Attempts)

	c.attempts += n

	if n > 1 {
		c.retries += n - 1
	}

	for _, attempt := range result.Attempts {
		c.totalAttemptDuration += attempt.Duration
	}

	return nil
}

func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	kinds := make(
		map[runtimecore.ErrorKind]int,
		len(c.errorsByKind),
	)

	for kind, count := range c.errorsByKind {
		kinds[kind] = count
	}

	return Snapshot{
		Completed: c.completed,
		Succeeded: c.succeeded,
		Failed:    c.failed,
		Cancelled: c.cancelled,

		Attempts: c.attempts,
		Retries:  c.retries,

		TotalAttemptDuration: c.totalAttemptDuration,

		ErrorsByKind: kinds,
	}
}
