package durable

import (
	"context"
	"errors"
	"fmt"
	"runtime-app/reliability15"
	"runtime-app/repodiag"
	"strings"
	"sync"
	"time"
)

// 不同 Provider 通过同一个工厂接口创建模型。
type ModelFactory func(
	provider string,
) (repodiag.Model, error)

// Scheduler 自动领取并运行 Agent Run。
type Scheduler struct {
	Queue Queue

	// 每个 Worker 进程使用唯一的 Session Token。
	Owner string

	// 本进程最大并发 Agent Run 数。
	Workers int

	// MaintenanceEvery defaults to one second and is independent of free slots.
	MaintenanceEvery time.Duration
	PollEvery        time.Duration
	TTL              time.Duration
	RenewEvery       time.Duration
	RunTimeout       time.Duration

	Models ModelFactory

	// Optional cancellation-aware initialization; preserves the legacy factory.
	ModelsWithContext func(context.Context, string) (repodiag.Model, error)

	Logf func(string, ...any)

	// 仅用于故障注入或实验观察。
	AfterSave func(Snapshot) error

	CodingRun func(
		context.Context,
		ClaimedRun,
	) (Snapshot, error)
}

func (s *Scheduler) log(
	format string,
	args ...any,
) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Scheduler) validate() error {
	if s.Queue.Pool == nil ||
		s.Owner == "" ||
		(s.Models == nil && s.ModelsWithContext == nil) {
		return errors.New(
			"queue pool, owner and model factory required",
		)
	}

	if s.Workers < 1 || s.Workers > 64 {
		return errors.New(
			"workers must be 1..64",
		)
	}

	if s.PollEvery < 10*time.Millisecond ||
		s.RunTimeout <= 0 {
		return errors.New(
			"invalid poll or run timeout",
		)
	}

	if err := validateLeaseInput(
		"queue",
		s.Owner,
		s.TTL,
	); err != nil {
		return err
	}

	if s.RenewEvery <= 0 ||
		s.RenewEvery*2 >= s.TTL {
		return errors.New(
			"renew interval must be less than half of lease TTL",
		)
	}

	return nil
}

// Run 是常驻调度循环。
//
// 收到 Context Cancellation 后：
// 1. 停止领取新任务
// 2. 取消正在执行的 Run
// 3. 等待已启动的 goroutine 退出
func (s *Scheduler) Run(
	ctx context.Context,
) error {
	if err := s.validate(); err != nil {
		return err
	}

	slots := make(
		chan struct{},
		s.Workers,
	)

	var wg sync.WaitGroup

	ticker := time.NewTicker(
		s.PollEvery,
	)
	defer ticker.Stop()

	maintenanceEvery := s.MaintenanceEvery
	if maintenanceEvery == 0 {
		maintenanceEvery = time.Second
	}
	if maintenanceEvery < 10*time.Millisecond {
		return errors.New("maintenance interval must be >=10ms")
	}
	var maintenance *reliability15.Store
	var nextSweep time.Time
	for {
		if ctx.Err() != nil {
			wg.Wait()
			return nil
		}
		if !time.Now().Before(nextSweep) {
			nextSweep = time.Now().Add(maintenanceEvery)
			sweepCtx, cancelSweep := context.WithTimeout(ctx, 3*time.Second)
			var maintenanceErr error
			if maintenance == nil {
				maintenance, maintenanceErr = reliability15.NewStore(s.Queue.Pool, "day12")
			}
			if maintenanceErr == nil {
				var count int
				count, maintenanceErr = maintenance.SweepExhausted(sweepCtx, 100)
				if count > 0 {
					s.log("SWEEP_EXHAUSTED count=%d", count)
				}
			}
			cancelSweep()
			if maintenanceErr != nil && ctx.Err() == nil {
				s.log("SWEEP_ERROR err=%v", maintenanceErr)
			}
		}
		if ctx.Err() != nil {
			wg.Wait()
			return nil
		}

		// 当前空闲的 Agent 执行槽。
		free := s.Workers - len(slots)

		if free > 0 {
			jobs, err := s.Queue.ClaimBatch(
				ctx,
				s.Owner,
				free,
				s.TTL,
			)

			if err != nil {
				if ctx.Err() != nil {
					wg.Wait()
					return nil
				}

				s.log(
					"CLAIM_ERROR err=%v",
					err,
				)
			} else {
				for _, job := range jobs {
					slots <- struct{}{}
					wg.Add(1)

					go func(job ClaimedRun) {
						defer wg.Done()

						// 无论成功或失败，都释放执行槽。
						defer func() {
							<-slots
						}()

						s.execute(ctx, job)
					}(job)
				}
			}
		}

		select {
		case <-ctx.Done():
			wg.Wait()
			return nil

		case <-ticker.C:
		}
	}
}

// 指数退避：1s、2s、4s、8s……
// 最大限制为 30s。
func retryDelay(
	retries int,
) time.Duration {
	if retries < 0 {
		retries = 0
	}

	if retries > 5 {
		retries = 5
	}

	wait := time.Second *
		time.Duration(1<<uint(retries))

	if wait > 30*time.Second {
		return 30 * time.Second
	}

	return wait
}

// execute 运行一个已经成功领取的 Agent Run。
func (s *Scheduler) execute(
	parent context.Context,
	job ClaimedRun,
) {
	s.log(
		"CLAIMED run=%s epoch=%d revision=load owner=%s",
		job.RunID,
		job.Lease.Epoch,
		s.Owner,
	)

	ctx, cancel := context.WithTimeout(
		parent,
		s.RunTimeout,
	)
	defer cancel()

	manager := LeaseManager{
		Pool: s.Queue.Pool,
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	heartbeatDone := make(
		chan struct{},
	)

	// 独立 goroutine 持续 Renew。
	go func() {
		defer close(heartbeatDone)

		ticker := time.NewTicker(
			s.RenewEvery,
		)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return

			case <-ticker.C:
				_, e := manager.Renew(
					runCtx,
					job.Lease,
					s.TTL,
				)

				if e != nil {
					s.log(
						"LEASE_LOST run=%s epoch=%d err=%v",
						job.RunID,
						job.Lease.Epoch,
						e,
					)

					cancelRun()
					return
				}
			}
		}
	}()

	var stopOnce sync.Once
	stopHeartbeat := func() { stopOnce.Do(func() { cancelRun(); <-heartbeatDone }) }
	defer stopHeartbeat()
	// Coding Run uses the same ClaimBatch,
	// Lease heartbeat and Scheduler lifecycle.
	if strings.HasPrefix(job.Provider, "coding:") {

		if s.CodingRun == nil {
			stopHeartbeat()

			s.finishError(
				job,
				errors.New(
					"coding handler is not configured",
				),
			)
			return
		}

		result, runErr := s.CodingRun(
			runCtx,
			job,
		)

		stopHeartbeat()

		if runErr != nil {
			s.finishError(job, runErr)
			return
		}

		s.log(
			"FINAL_CODING run=%s epoch=%d "+
				"revision=%d status=%s",

			job.RunID,
			job.Lease.Epoch,
			result.Revision,
			result.Status,
		)

		cleanup, done := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer done()

		if err := manager.Release(
			cleanup,
			job.Lease,
		); err != nil {

			s.log(
				"RELEASE_ERROR run=%s err=%v",
				job.RunID,
				err,
			)
		}

		return
	}
	// 创建与 Run 元数据匹配的模型。
	var model repodiag.Model
	var err error
	if s.ModelsWithContext != nil {
		model, err = s.ModelsWithContext(runCtx, job.Provider)
	} else {
		model, err = s.Models(job.Provider)
	}
	if runCtx.Err() != nil {
		err = runCtx.Err()
	}
	if err != nil {
		stopHeartbeat()
		s.finishError(
			job,
			fmt.Errorf("make model: %w", err),
		)
		return
	}

	if err = manager.Check(runCtx, job.Lease); err != nil {
		stopHeartbeat()
		s.finishError(job, err)
		return
	}
	// 创建真实仓库工作区。
	workspace, err := repodiag.NewWorkspace(
		job.Repo,
	)
	if err != nil {
		stopHeartbeat()
		s.finishError(
			job,
			fmt.Errorf("workspace: %w", err),
		)
		return
	}

	if runCtx.Err() != nil {
		stopHeartbeat()
		s.finishError(job, runCtx.Err())
		return
	}
	// 继续使用 Day13 的 FencedStore。
	runner := &Runner{
		Store: FencedStore{
			Pool:  s.Queue.Pool,
			Lease: job.Lease,
		},

		Model:     model,
		Workspace: workspace,
		Provider:  job.Provider,

		MaxTurns: 8,
		MaxCalls: 12,

		Guard: func(
			c context.Context,
		) error {
			return manager.Check(
				c,
				job.Lease,
			)
		},

		AfterSave: func(ss Snapshot) error {
			s.log(
				"COMMITTED run=%s epoch=%d revision=%d status=%s",
				ss.RunID,
				job.Lease.Epoch,
				ss.Revision,
				ss.Status,
			)

			if s.AfterSave != nil {
				return s.AfterSave(ss)
			}

			return nil
		},
	}

	// 从最新 Checkpoint 开始运行。
	result, runErr := runner.Run(
		runCtx,
		job.RunID,
		job.Repo,
		job.Goal,
	)

	// Agent 完成后停止 Heartbeat。
	stopHeartbeat()

	if runErr != nil {
		s.finishError(
			job,
			runErr,
		)
		return
	}

	s.log(
		"FINAL run=%s epoch=%d revision=%d status=%s",
		result.RunID,
		job.Lease.Epoch,
		result.Revision,
		result.Status,
	)

	// 终态 Run 不再满足队列领取条件。
	cleanup, done := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer done()

	if err := manager.Release(
		cleanup,
		job.Lease,
	); err != nil {
		s.log(
			"RELEASE_ERROR run=%s err=%v",
			job.RunID,
			err,
		)
	}
}

// 如果执行失败，在仍拥有 Lease 时重新安排任务。
// 如果 Lease 已经丢失，不能再修改其他 Worker 的任务。
func (s *Scheduler) finishError(
	job ClaimedRun,
	runErr error,
) {
	cause := schedulerFailure(runErr)
	kind := reliability15.ClassifyFailure(cause)
	s.log("RUN_ERROR run=%s epoch=%d kind=%s", job.RunID, job.Lease.Epoch, kind)
	// Ignore cancellation, lost ownership and revision conflicts. The next owner
	// reconciles durable state; an old owner cannot reschedule the new lease.
	if kind == reliability15.FailureInterrupted || kind == reliability15.FailureLeaseLost || kind == reliability15.FailureConflict {
		return
	}
	cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	store, err := reliability15.NewStore(s.Queue.Pool, "day12")
	if err == nil {
		_, err = store.FinishFailure(cleanup, reliability15.RunRef{ID: job.RunID, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch},
			cause, "scheduler failure: "+string(kind), time.Second, 30*time.Second)
	}
	if err != nil && !errors.Is(err, reliability15.ErrLeaseLost) && !errors.Is(err, reliability15.ErrNoAction) {
		s.log("FINISH_ERROR run=%s err=%v", job.RunID, err)
	}
}
