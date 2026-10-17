package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"runtime-app/executor"
	"runtime-app/observability"
	"runtime-app/runtimecore"
	"runtime-app/task"
)

func main() {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	r := runtimecore.NewRuntime(
		executor.UppercaseExecutor{},
		3,
		4,
	)

	metrics := observability.NewCollector()

	r.Start(ctx)

	const producerCount = 2
	const tasksPerProducer = 10

	var wg sync.WaitGroup

	submitErrors := make(
		chan error,
		producerCount*tasksPerProducer,
	)

	for producerID := 0; producerID < producerCount; producerID++ {

		wg.Add(1)

		go func(p int) {
			defer wg.Done()

			for i := 0; i < tasksPerProducer; i++ {
				id := p*tasksPerProducer + i + 1

				job := task.Task{
					ID:      id,
					Name:    fmt.Sprintf("task-%d", id),
					Payload: "hello agent",
				}

				err := r.Submit(ctx, job)

				submitErrors <- err

				if err != nil {
					return
				}
			}
		}(producerID)
	}

	// 两个 Producer 都结束后，才关闭 Runtime。
	go func() {
		wg.Wait()

		r.Close()

		close(submitErrors)
	}()

	// Main 持续消费 Result，避免 Worker 阻塞。
	for result := range r.Results() {
		if err := metrics.Record(result); err != nil {
			fmt.Println("metrics error:", err)
		}
	}

	submitted := 0
	rejected := 0

	for err := range submitErrors {
		if err == nil {
			submitted++
		} else {
			rejected++
			fmt.Println("submit error:", err)
		}
	}

	snapshot := metrics.Snapshot()

	fmt.Println()
	fmt.Println("===== DAY 7 SUMMARY =====")

	fmt.Println("submitted:", submitted)
	fmt.Println("rejected:", rejected)
	fmt.Println("completed:", snapshot.Completed)
	fmt.Println("succeeded:", snapshot.Succeeded)
	fmt.Println("failed:", snapshot.Failed)
	fmt.Println("cancelled:", snapshot.Cancelled)
	fmt.Println("attempts:", snapshot.Attempts)

	if submitted != snapshot.Completed {
		panic("accepted tasks != terminal results")
	}

}
