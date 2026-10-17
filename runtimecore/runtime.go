package runtimecore

import (
	"context"
	"errors"
	"runtime-app/executor"
	"runtime-app/task"
	"sync"
	"time"
)

type Runtime struct {
	executor    executor.Executor
	workerCount int

	tasks   chan task.Task
	results chan Result

	wg sync.WaitGroup

	states      *StateStore
	retryPolicy RetryPolicy

	submitMu sync.Mutex

	submissionsClosed bool

	submitting sync.WaitGroup

	closeOnce sync.Once
}

func NewRuntime(
	e executor.Executor,
	workerCount int,
	queueSize int,
) *Runtime {
	return &Runtime{
		executor:    e,
		workerCount: workerCount,

		tasks:   make(chan task.Task, queueSize),
		results: make(chan Result, queueSize),

		states: NewStateStore(),

		// 保持旧版本默认不重试。
		retryPolicy: RetryPolicy{
			MaxAttempts: 1,
			BaseDelay:   100 * time.Millisecond,
			MaxDelay:    time.Second,
		},
	}
}

func NewRuntimeWithRetryPolicy(
	e executor.Executor,
	workerCount int,
	queueSize int,
	p RetryPolicy,
) (*Runtime, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	r := NewRuntime(
		e,
		workerCount,
		queueSize,
	)

	r.retryPolicy = p

	return r, nil
}

func (r *Runtime) Start(ctx context.Context) {
	for workerID := 1; workerID <= r.workerCount; workerID++ {
		r.wg.Add(1)
		go r.worker(ctx, workerID)
	}

	go func() {
		r.wg.Wait()
		close(r.results)
	}()
}

func (r *Runtime) Results() <-chan Result {
	return r.results
}

func (r *Runtime) State(
	taskID int,
) (State, bool) {
	return r.states.Get(taskID)
}

var ErrRuntimeClosed = errors.New("runtime is closed")

func (r *Runtime) Submit(
	ctx context.Context,
	t task.Task,
) error {

	// 1. 已取消的 Context 不应开始新提交。
	if err := ctx.Err(); err != nil {
		return err
	}

	// 2. 获取提交控制锁。
	r.submitMu.Lock()

	// 3. 检查是否已经关闭提交入口。
	if r.submissionsClosed {
		r.submitMu.Unlock()
		return ErrRuntimeClosed
	}

	// 4. 登记一个正在执行的 Submit。
	r.submitting.Add(1)

	// 5. 释放锁，避免持锁发送 Channel。
	r.submitMu.Unlock()

	// 6. Submit 无论成功失败，都需要 Done。
	defer r.submitting.Done()

	// 7. 先登记 Pending，防止 Worker 抢先处理。
	if err := r.states.Register(t.ID); err != nil {
		return err
	}

	// 8. 尝试发送 Task。
	select {
	case r.tasks <- t:
		return nil

	case <-ctx.Done():
		// 未成功入队时撤销状态登记。
		r.states.forget(t.ID)
		return ctx.Err()
	}
}
func (r *Runtime) Close() {

	r.closeOnce.Do(func() {

		// 1. 关闭新的 Submit 入口。
		r.submitMu.Lock()

		r.submissionsClosed = true

		r.submitMu.Unlock()

		// 2. 等待已经进入 Submit 的调用结束。
		r.submitting.Wait()

		// 3. 确认不会再有 Submit 发送到 tasks，
		//    然后关闭输入 Channel。
		close(r.tasks)
	})
}
