package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"runtime-app/executor"
	"runtime-app/httpapi"
	"runtime-app/persistence"
	"runtime-app/runtimecore"
)

func main() {
	logger := slog.New(
		slog.NewJSONHandler(
			os.Stdout,
			nil,
		),
	)

	// 正式服务必须配置 PostgreSQL，避免重启后丢失内存任务。
	dsn, configErr := databaseURL()
	if configErr != nil {
		logger.Error("database_config_missing", "error", configErr)
		return
	}
	dbCtx, cancelDB := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := persistence.NewDB(dbCtx, dsn)
	cancelDB()
	if err != nil {
		logger.Error("database_connect_failed")
		return
	}
	defer db.Pool.Close()
	schemaCtx, cancelSchema := context.WithTimeout(context.Background(), 10*time.Second)
	err = db.EnsureTaskSchema(schemaCtx)
	cancelSchema()
	if err != nil {
		logger.Error("database_schema_failed", "error", err)
		return
	}
	logger.Info("postgres_connected")

	// 只注册演示用 uppercase Tool。
	registry := executor.NewRegistry()

	if err := registry.Register(
		"uppercase",
		executor.UppercaseExecutor{},
	); err != nil {
		logger.Error(
			"register_failed",
			"error", err,
		)
		return
	}

	// 先沿用 Day 7 已验收的 Runtime。
	r := runtimecore.NewRuntime(
		registry,
		4,
		8,
	)

	// Worker 使用服务级 Context。
	// 不依赖某一个 HTTP 请求的生命周期。
	serviceCtx, cancelService := context.WithCancel(
		context.Background(),
	)
	defer cancelService()

	r.Start(serviceCtx)

	service := httpapi.NewServiceWithStore(
		r,
		persistence.NewPostgresTaskStore(db),
		logger,
	)

	// 必须在接收 HTTP 请求前启动 Collector。
	service.StartCollector()

	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	server := &http.Server{
		Addr: address,

		Handler: service.Handler(),

		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErrors := make(chan error, 1)

	go func() {
		logger.Info(
			"http_server_started",
			"address", server.Addr,
		)

		serveErrors <- server.ListenAndServe()
	}()

	// Ctrl+C / SIGTERM 触发关闭流程。
	signals := make(chan os.Signal, 1)

	signal.Notify(
		signals,
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer signal.Stop(signals)

	select {
	case sig := <-signals:
		logger.Info(
			"shutdown_requested",
			"signal", sig.String(),
		)

	case err := <-serveErrors:
		if err != nil &&
			!errors.Is(err, http.ErrServerClosed) {

			logger.Error(
				"http_server_failed",
				"error", err,
			)
		}
	}

	// 第一步：停止 HTTP 接受新的请求，
	// 并尝试等待正在处理的 Handler。
	shutdownCtx, cancelShutdown := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancelShutdown()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn(
			"http_shutdown_timeout",
			"error", err,
		)

		// 结束剩余连接，促使请求 Context 取消。
		_ = server.Close()
	}

	// 第二步：安全关闭 Runtime 的 Task 输入队列。
	r.Close()

	// 第三步：继续 Drain 已接受 Task 的 Result。
	<-service.Done()

	// 第四步：完整关闭后释放服务 Context。
	cancelService()

	logger.Info("day8_server_stopped")
}
