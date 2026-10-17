package main

import (
	"context"
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

func main() {
	// 1. 命令行参数
	runID := flag.String(
		"id",
		"day12-demo",
		"stable Agent Run ID",
	)

	repoPath := flag.String(
		"repo",
		"",
		"path to a trusted local Go repository",
	)

	goal := flag.String(
		"goal",
		"检查 Go 仓库的测试结果",
		"Agent diagnosis goal",
	)

	mode := flag.String(
		"mode",
		"demo",
		"model: demo or deepseek",
	)

	pauseAfter := flag.Int64(
		"pause-after",
		-1,
		"pause after committing this checkpoint revision",
	)

	pauseDuration := flag.Duration(
		"pause",
		60*time.Second,
		"how long to pause after a checkpoint",
	)

	runTimeout := flag.Duration(
		"timeout",
		5*time.Minute,
		"maximum time for this process run",
	)

	flag.Parse()

	if *repoPath == "" {
		log.Fatal(
			"missing required argument: -repo",
		)
	}

	if *runTimeout <= 0 {
		log.Fatal(
			"-timeout must be positive",
		)
	}

	// 2. 获取 PostgreSQL 连接字符串
	dsn := os.Getenv("DAY12_DATABASE_URL")

	if dsn == "" {
		log.Fatal(
			"DAY12_DATABASE_URL is not set",
		)
	}

	// 3. 获取仓库真实路径
	root, err := filepath.Abs(*repoPath)
	if err != nil {
		log.Fatal("resolve repository path: ", err)
	}

	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		log.Fatal("resolve repository symlinks: ", err)
	}

	workspace, err := repodiag.NewWorkspace(root)
	if err != nil {
		log.Fatal("create workspace: ", err)
	}

	// 4. 选择 Model Adapter
	var model repodiag.Model
	var provider string

	switch *mode {
	case "demo":
		model = repodiag.DemoModel{}
		provider = "demo"

	case "deepseek":
		apiKey := os.Getenv("DEEPSEEK_API_KEY")

		if apiKey == "" {
			log.Fatal(
				"DEEPSEEK_API_KEY is not set",
			)
		}

		modelID := os.Getenv("DEEPSEEK_MODEL")
		if modelID == "" {
			modelID = "deepseek-flash"
		}

		model = repodiag.DeepSeekModel{
			APIKey: apiKey,
			Model:  modelID,
		}

		provider = "deepseek:" + modelID

	default:
		log.Fatalf(
			"unsupported mode: %s",
			*mode,
		)
	}

	// 5. 创建整个 Run 的 Context
	ctx, cancel := context.WithTimeout(
		context.Background(),
		*runTimeout,
	)
	defer cancel()

	// 6. PostgreSQL Connection Pool
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal("create PostgreSQL pool: ", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatal("PostgreSQL ping failed: ", err)
	}

	// 7. 创建真正的 PostgreSQL Store
	store := durable.PostgresStore{
		Pool: pool,
	}

	// 8. 创建 Durable Runner
	runner := &durable.Runner{
		Store:     store,
		Model:     model,
		Workspace: workspace,
		Provider:  provider,

		MaxTurns: 8,
		MaxCalls: 12,

		// 仅用于测试 Checkpoint 之后的进程中断。
		AfterSave: func(s durable.Snapshot) error {
			fmt.Printf(
				"COMMITTED run=%s revision=%d "+
					"turns=%d calls=%d status=%s\n",
				s.RunID,
				s.Revision,
				s.Turns,
				s.Calls,
				s.Status,
			)

			if s.Revision != *pauseAfter {
				return nil
			}

			pid := os.Getpid()

			fmt.Printf(
				"\nPAUSED after committed revision %d\n",
				s.Revision,
			)
			fmt.Printf("PID=%d\n", pid)

			fmt.Printf(
				"To simulate a crash, open another terminal:\n",
			)
			fmt.Printf("kill -9 %d\n\n", pid)

			timer := time.NewTimer(*pauseDuration)
			defer timer.Stop()

			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-timer.C:
				fmt.Println("PAUSE FINISHED")
				return nil
			}
		},
	}

	// 9. 打印执行信息
	fmt.Println("========== DAY 12 DURABLE AGENT ==========")
	fmt.Printf("RunID:     %s\n", *runID)
	fmt.Printf("Repository:%s\n", root)
	fmt.Printf("Provider:  %s\n", provider)
	fmt.Printf("Goal:      %s\n", *goal)
	fmt.Println("==========================================")

	// 10. 创建或恢复 Agent Run
	result, err := runner.Run(
		ctx,
		*runID,
		root,
		*goal,
	)

	if err != nil {
		if errors.Is(err, durable.ErrConflict) {
			log.Println(
				"checkpoint conflict: another writer " +
					"may have updated this Run",
			)
		}

		log.Fatalf(
			"durable run failed: %v",
			err,
		)
	}

	// 11. 最终状态
	fmt.Println()
	fmt.Println("========== FINAL RESULT ==========")

	fmt.Printf(
		"RunID:    %s\n",
		result.RunID,
	)

	fmt.Printf(
		"Status:   %s\n",
		result.Status,
	)

	fmt.Printf(
		"Revision: %d\n",
		result.Revision,
	)

	fmt.Printf(
		"Turns:    %d\n",
		result.Turns,
	)

	fmt.Printf(
		"Calls:    %d\n",
		result.Calls,
	)

	fmt.Println()
	fmt.Println(result.Final)
	fmt.Println("========== RUN COMPLETED ==========")

	if result.Status != durable.Succeeded {
		os.Exit(1)
	}
}
