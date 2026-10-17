package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/durable"
	"runtime-app/repodiag"
)

// 每次进程启动时生成不同的 Owner Session Token。
func sessionToken() (string, error) {
	var b [16]byte

	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(b[:]), nil
}

func main() {
	if err := run(); err != nil {
		log.Printf("day13 failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	id := flag.String(
		"id", "",
		"stable Agent Run ID (required)",
	)
	repo := flag.String(
		"repo", "",
		"trusted local repository (required)",
	)
	goal := flag.String(
		"goal",
		"检查 Go 仓库的测试结果",
		"must match when resuming",
	)
	mode := flag.String(
		"mode", "demo",
		"demo or deepseek",
	)
	worker := flag.String(
		"worker", "worker-a",
		"human-readable worker label",
	)
	ttl := flag.Duration(
		"ttl", 6*time.Second,
		"database lease TTL",
	)
	renewEvery := flag.Duration(
		"renew", time.Second,
		"heartbeat renewal interval",
	)
	timeout := flag.Duration(
		"timeout", 5*time.Minute,
		"overall run timeout",
	)
	pauseAfter := flag.Int64(
		"pause-after", -1,
		"pause after a committed revision",
	)
	pause := flag.Duration(
		"pause", 120*time.Second,
		"fault injection pause duration",
	)

	flag.Parse()

	if *id == "" || *repo == "" {
		return errors.New("-id and -repo required")
	}

	if *ttl < time.Millisecond ||
		*renewEvery <= 0 ||
		*renewEvery >= *ttl/2 {
		return errors.New(
			"require ttl >= 1ms and 0 < renew < ttl/2",
		)
	}

	if *timeout <= 0 || *pause < 0 {
		return errors.New(
			"invalid timeout or pause",
		)
	}

	dsn := os.Getenv("DAY12_DATABASE_URL")
	if dsn == "" {
		return errors.New("DAY12_DATABASE_URL required")
	}

	// Workspace 使用规范化后的真实路径。
	root, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}

	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}

	workspace, err := repodiag.NewWorkspace(root)
	if err != nil {
		return err
	}

	// 选择 Model。
	var model repodiag.Model
	provider := *mode

	switch *mode {
	case "demo":
		model = repodiag.DemoModel{}

	case "deepseek":
		key := os.Getenv("DEEPSEEK_API_KEY")
		if key == "" {
			return errors.New(
				"DEEPSEEK_API_KEY required",
			)
		}

		modelID := os.Getenv("DEEPSEEK_MODEL")
		if modelID == "" {
			modelID = "deepseek-flash"
		}

		provider = "deepseek:" + modelID

		model = repodiag.DeepSeekModel{
			APIKey: key,
			Model:  modelID,
		}

	default:
		return fmt.Errorf(
			"unsupported mode %q", *mode,
		)
	}

	ctx, stop := context.WithTimeout(
		context.Background(),
		*timeout,
	)
	defer stop()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}

	// 第一步：初始化或加载 Run。
	// 此时尚未进入 Runner，也尚未执行任何 Tool。
	base := durable.PostgresStore{
		Pool: pool,
	}

	old, err := base.Load(ctx, *id)

	if errors.Is(err, durable.ErrNotFound) {
		userMessage, e := json.Marshal(
			map[string]string{
				"role":    "user",
				"content": *goal,
			},
		)
		if e != nil {
			return e
		}

		initial := durable.Snapshot{
			RunID:    *id,
			Repo:     root,
			Provider: provider,
			Goal:     *goal,
			Status:   durable.Running,
			History: []json.RawMessage{
				userMessage,
			},
		}

		e = base.Create(ctx, initial)
		if e != nil && !errors.Is(
			e, durable.ErrExists,
		) {
			return e
		}

		old, err = base.Load(ctx, *id)
	}

	if err != nil {
		return err
	}

	// 同一个 RunID 不允许被复用为其他任务。
	if old.Repo != root ||
		old.Provider != provider ||
		old.Goal != *goal {
		return errors.New(
			"RunID belongs to a different repository, model, or goal",
		)
	}

	// 已完成的 Run 不应该重新执行。
	if old.Status != durable.Running {
		fmt.Printf(
			"RUN ALREADY FINAL run=%s status=%s revision=%d\n%s\n",
			old.RunID,
			old.Status,
			old.Revision,
			old.Final,
		)
		return nil
	}

	// 第二步：领取 Lease。
	token, err := sessionToken()
	if err != nil {
		return err
	}

	manager := durable.LeaseManager{
		Pool: pool,
	}

	lease, err := manager.Claim(
		ctx, *id, token, *ttl,
	)
	if err != nil {
		return err
	}

	fmt.Printf(
		"CLAIMED worker=%s run=%s epoch=%d owner=%s lease_until=%s\n",
		*worker,
		lease.RunID,
		lease.Epoch,
		lease.Owner,
		lease.Until.Format(time.RFC3339Nano),
	)

	// 正常退出时仅释放当前 Session 的租约。
	// SIGKILL 时 defer 不会执行。
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancel()

		if err := manager.Release(
			cleanupCtx, lease,
		); err != nil {
			log.Printf(
				"release lease warning: %v", err,
			)
		}
	}()

	// 第三步：创建可取消的 Run Context。
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	heartbeatDone := make(chan struct{})
	heartbeatError := make(chan error, 1)

	// 第四步：独立 Heartbeat goroutine。
	go func() {
		defer close(heartbeatDone)

		ticker := time.NewTicker(*renewEvery)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return

			case <-ticker.C:
				until, e := manager.Renew(
					runCtx, lease, *ttl,
				)

				if e != nil {
					select {
					case heartbeatError <- e:
					default:
					}

					cancelRun()
					return
				}

				fmt.Printf(
					"RENEWED run=%s epoch=%d until=%s\n",
					lease.RunID,
					lease.Epoch,
					until.Format(time.RFC3339Nano),
				)
			}
		}
	}()

	// 第五步：创建 Fenced Runner。
	runner := &durable.Runner{
		Store: durable.FencedStore{
			Pool:  pool,
			Lease: lease,
		},

		Model:     model,
		Workspace: workspace,
		Provider:  provider,

		MaxTurns: 8,
		MaxCalls: 12,

		Guard: func(c context.Context) error {
			return manager.Check(c, lease)
		},

		AfterSave: func(s durable.Snapshot) error {
			fmt.Printf(
				"COMMITTED run=%s epoch=%d revision=%d turns=%d calls=%d status=%s\n",
				s.RunID,
				lease.Epoch,
				s.Revision,
				s.Turns,
				s.Calls,
				s.Status,
			)

			if s.Revision != *pauseAfter {
				return nil
			}

			fmt.Printf(
				"PAUSED pid=%d after revision=%d; use kill -9 %d in another terminal\n",
				os.Getpid(),
				s.Revision,
				os.Getpid(),
			)

			timer := time.NewTimer(*pause)
			defer timer.Stop()

			select {
			case <-runCtx.Done():
				return runCtx.Err()

			case <-timer.C:
				return nil
			}
		},
	}

	// 第六步：运行或恢复 Agent。
	result, runErr := runner.Run(
		runCtx, *id, root, *goal,
	)

	// Agent 已结束，停止 Heartbeat 并等待退出。
	cancelRun()
	<-heartbeatDone

	if runErr != nil {
		select {
		case heartbeatErr := <-heartbeatError:
			return fmt.Errorf(
				"heartbeat failed (%v); agent stopped: %w",
				heartbeatErr, runErr,
			)
		default:
		}
		return runErr
	}

	fmt.Printf(
		"FINAL run=%s status=%s epoch=%d revision=%d turns=%d calls=%d\n%s\n",
		result.RunID,
		result.Status,
		lease.Epoch,
		result.Revision,
		result.Turns,
		result.Calls,
		result.Final,
	)

	if result.Status != durable.Succeeded {
		return fmt.Errorf(
			"run ended with %s",
			result.Status,
		)
	}

	return nil
}
