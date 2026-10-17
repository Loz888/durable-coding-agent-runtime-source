package main

import (
	"context"
	"fmt"
	"time"

	"runtime-app/executor"
	"runtime-app/observability"
	"runtime-app/runtimecore"
	"runtime-app/task"
)

const (
	TaskCount = 40
	QueueSize = 8
	TaskDelay = 20 * time.Millisecond
)

// fixedDelayExecutor 模拟固定耗时的任务。
// 每次 Execute 正常运行时等待 20ms。
type fixedDelayExecutor struct {
	Delay time.Duration
}

func (e fixedDelayExecutor) Execute(
	ctx context.Context,
	t task.Task,
) (executor.Output, error) {

	timer := time.NewTimer(e.Delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return executor.Output{
			ExitCode: -1,
		}, ctx.Err()

	case <-timer.C:
		return executor.Output{
			ExitCode: 0,
			Stdout:   "ok",
		}, nil
	}
}

// ExperimentResult 保存单轮实验结果。
type ExperimentResult struct {
	WorkerCount int
	Completed   int
	Elapsed     time.Duration
	Throughput  float64

	Attempts int
	Retries  int
}

// runExperiment 执行一次完整实验。
func runExperiment(
	workerCount int,
) (ExperimentResult, error) {

	ctx := context.Background()

	// 1. 创建模拟 Executor
	e := fixedDelayExecutor{
		Delay: TaskDelay,
	}

	// 2. 创建 Runtime
	r := runtimecore.NewRuntime(
		e,
		workerCount,
		QueueSize,
	)

	// 3. 创建指标收集器
	collector := observability.NewCollector()

	// 4. 开始计时
	start := time.Now()

	// 5. 启动 Worker Pool
	r.Start(ctx)

	// 6. Producer 提交任务
	producerErrCh := make(chan error, 1)

	go func() {
		defer r.Close()

		for i := 1; i <= TaskCount; i++ {
			t := task.Task{
				ID:   i,
				Name: fmt.Sprintf("task-%d", i),
			}

			if err := r.Submit(ctx, t); err != nil {
				producerErrCh <- fmt.Errorf(
					"submit task %d: %w",
					i,
					err,
				)
				return
			}
		}

		producerErrCh <- nil
	}()

	// 7. Consumer 持续接收 Result
	// 必须与 Producer 并发进行，防止队列阻塞。
	var recordErr error

	for result := range r.Results() {
		err := collector.Record(result)

		if err != nil && recordErr == nil {
			recordErr = err
		}

		// 即使 Record 出错，也继续接收剩余结果，
		// 防止 Worker 因发送 Result 而阻塞。
	}

	// 8. 所有结果接收完毕后停止计时
	elapsed := time.Since(start)

	// 9. 检查 Producer 错误
	if err := <-producerErrCh; err != nil {
		return ExperimentResult{}, err
	}

	if recordErr != nil {
		return ExperimentResult{}, recordErr
	}

	// 10. 获取统计快照
	snapshot := collector.Snapshot()

	// 11. 验证所有任务都已经完成
	if snapshot.Completed != TaskCount {
		return ExperimentResult{}, fmt.Errorf(
			"workerCount=%d: expected %d completed, got %d",
			workerCount,
			TaskCount,
			snapshot.Completed,
		)
	}

	// 12. 计算吞吐量
	throughput := float64(snapshot.Completed) /
		elapsed.Seconds()

	return ExperimentResult{
		WorkerCount: workerCount,
		Completed:   snapshot.Completed,
		Elapsed:     elapsed,
		Throughput:  throughput,
		Attempts:    snapshot.Attempts,
		Retries:     snapshot.Retries,
	}, nil
}

func main() {

	workerCounts := []int{1, 2, 4}

	fmt.Println("=== WorkerCount Performance Experiment ===")

	fmt.Printf(
		"TaskCount=%d QueueSize=%d TaskDelay=%s\n\n",
		TaskCount,
		QueueSize,
		TaskDelay,
	)

	fmt.Println("| Workers | Completed | Elapsed | Throughput (tasks/s) |")
	fmt.Println("|---------|-----------|---------|----------------------|")

	for _, workers := range workerCounts {

		result, err := runExperiment(workers)

		if err != nil {
			fmt.Printf(
				"experiment failed (workers=%d): %v\n",
				workers,
				err,
			)
			return
		}

		fmt.Printf(
			"| %-7d | %-9d | %-7s | %-20.2f |\n",
			result.WorkerCount,
			result.Completed,
			result.Elapsed.Round(time.Millisecond),
			result.Throughput,
		)
	}
}
