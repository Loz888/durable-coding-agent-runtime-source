package runtimecore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"runtime-app/executor"
	"runtime-app/task"
)

func TestRuntimeCollectsAllResults(t *testing.T) {
	for _, queueSize := range []int{0, 1, 2} {
		t.Run(
			fmt.Sprintf("queue_%d", queueSize),
			func(t *testing.T) {
				ctx, cancel := context.WithTimeout(
					context.Background(),
					5*time.Second,
				)
				defer cancel()

				r := NewRuntime(
					executor.DelayExecutor{
						Delay: 10 * time.Millisecond,
					},
					3,
					queueSize,
				)

				r.Start(ctx)

				const taskCount = 20

				submitDone := make(chan error, 1)

				go func() {
					defer r.Close()

					for id := 1; id <= taskCount; id++ {
						err := r.Submit(
							ctx,
							task.New(id, "test-task", "hello"),
						)

						if err != nil {
							submitDone <- err
							return
						}
					}

					submitDone <- nil
				}()

				seen := make(map[int]bool)

				for result := range r.Results() {
					if result.Err != nil {
						t.Errorf(
							"task %d failed: %v",
							result.TaskID,
							result.Err,
						)
					}

					if seen[result.TaskID] {
						t.Errorf(
							"duplicate task ID: %d",
							result.TaskID,
						)
					}

					seen[result.TaskID] = true
				}

				if err := <-submitDone; err != nil {
					t.Fatalf(
						"submit failed: %v",
						err,
					)
				}

				if len(seen) != taskCount {
					t.Fatalf(
						"got %d unique results, want %d",
						len(seen),
						taskCount,
					)
				}
			},
		)
	}
}
