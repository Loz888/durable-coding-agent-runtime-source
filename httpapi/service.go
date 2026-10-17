package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"runtime-app/observability"
	"runtime-app/persistence"
	"runtime-app/runtimecore"
	"runtime-app/task"
)

type Service struct {
	runtime   *runtimecore.Runtime
	store     persistence.TaskStore
	collector *ResultCollector
	metrics   *observability.Collector
	logger    *slog.Logger

	collectorOnce sync.Once
	done          chan struct{}
}

func NewService(r *runtimecore.Runtime, logger *slog.Logger) *Service {
	return NewServiceWithStore(r, &resultStoreAdapter{store: NewResultStore()}, logger)
}

// NewServiceWithStore shares one storage backend across submission, collection and queries.
func NewServiceWithStore(r *runtimecore.Runtime, store persistence.TaskStore, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		runtime: r, store: store, collector: NewResultCollector(store),
		metrics: observability.NewCollector(), logger: logger, done: make(chan struct{}),
	}
}

// StartCollector 启动唯一的后台 Result 消费者。
func (s *Service) StartCollector() {
	s.collectorOnce.Do(func() {
		go s.collectResults()
	})
}

func (s *Service) collectResults() {
	defer close(s.done)

	for result := range s.runtime.Results() {
		if err := s.metrics.Record(result); err != nil {
			s.logger.Error(
				"metrics_record_failed",
				"task_id", result.TaskID,
				"error", err,
			)
		}

		if err := s.collector.Record(context.Background(), result); err != nil {
			s.logger.Error(
				"result_store_failed",
				"task_id", result.TaskID,
				"error", err,
			)
		}

		observability.LogResult(s.logger, result)
	}
}

// Done 在 Results Channel 完全关闭且消费结束后关闭。
func (s *Service) Done() <-chan struct{} {
	return s.done
}

// Handler 构造 HTTP 路由。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"POST /tasks",
		s.postTask,
	)

	mux.HandleFunc(
		"GET /tasks/{id}",
		s.getTask,
	)

	mux.HandleFunc(
		"GET /metrics",
		s.getMetrics,
	)

	return mux
}

type submitRequest struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	ToolName string `json:"tool_name"`
	Payload  string `json:"payload"`
}

type submitResponse struct {
	TaskID   int  `json:"task_id"`
	Accepted bool `json:"accepted"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(
	w http.ResponseWriter,
	status int,
	data any,
) {
	w.Header().Set(
		"Content-Type",
		"application/json; charset=utf-8",
	)

	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(data)
}

func (s *Service) postTask(
	w http.ResponseWriter,
	r *http.Request,
) {
	defer r.Body.Close()

	// 限制请求体大小，避免无限制读取。
	r.Body = http.MaxBytesReader(
		w,
		r.Body,
		4096,
	)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var req submitRequest

	if err := decoder.Decode(&req); err != nil {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "invalid JSON body"},
		)
		return
	}

	// 不允许一个请求包含多个 JSON 对象。
	var extra any

	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "unexpected trailing JSON"},
		)
		return
	}

	// 参数校验。
	if req.ID <= 0 ||
		strings.TrimSpace(req.Name) == "" ||
		len(req.Name) > 128 ||
		len(req.Payload) == 0 ||
		len(req.Payload) > 1024 {

		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "invalid task fields"},
		)
		return
	}

	// Day 8 只允许安全的演示 Tool。
	// 不允许通过 HTTP 访问 ShellExecutor。
	if req.ToolName != "uppercase" {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "unsupported tool"},
		)
		return
	}

	t := task.Task{
		ID:       req.ID,
		Name:     req.Name,
		ToolName: req.ToolName,
		Payload:  req.Payload,
	}

	// 先保留 TaskID，防止结果先到达。
	if err := s.store.Create(r.Context(), persistence.TaskRecord{
		ID: int64(t.ID), Name: t.Name, ToolName: t.ToolName, State: "pending",
	}); err != nil {
		if errors.Is(err, persistence.ErrDuplicateTask) {
			writeJSON(w, http.StatusConflict, errorResponse{Error: "duplicate task id"})
		} else {
			s.logger.Error("task_create_failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "task storage failed"})
		}
		return
	}

	// 这里的请求 Context 只控制提交阶段。
	err := s.runtime.Submit(r.Context(), t)

	if err != nil {
		// 没有成功接受 Task，撤销 HTTP 层记录。
		if rollbackErr := s.store.Delete(context.Background(), int64(t.ID)); rollbackErr != nil {
			s.logger.Error("task_rollback_failed", "task_id", t.ID, "error", rollbackErr)
		}

		switch {
		case errors.Is(
			err,
			runtimecore.ErrRuntimeClosed,
		):
			writeJSON(
				w,
				http.StatusServiceUnavailable,
				errorResponse{
					Error: "runtime is closed",
				},
			)

		case errors.Is(err, context.Canceled),
			errors.Is(err, context.DeadlineExceeded):
			writeJSON(
				w,
				http.StatusRequestTimeout,
				errorResponse{
					Error: "task submission interrupted",
				},
			)

		default:
			s.logger.Error(
				"submit_failed",
				"task_id", t.ID,
				"error", err,
			)

			writeJSON(
				w,
				http.StatusInternalServerError,
				errorResponse{
					Error: "task submission failed",
				},
			)
		}

		return
	}

	w.Header().Set(
		"Location",
		fmt.Sprintf("/tasks/%d", t.ID),
	)

	writeJSON(
		w,
		http.StatusAccepted,
		submitResponse{
			TaskID:   t.ID,
			Accepted: true,
		},
	)
}

func (s *Service) getTask(
	w http.ResponseWriter,
	r *http.Request,
) {
	id, err := strconv.Atoi(
		r.PathValue("id"),
	)

	if err != nil || id <= 0 {
		writeJSON(
			w,
			http.StatusBadRequest,
			errorResponse{Error: "invalid task id"},
		)
		return
	}

	stored, err := s.store.Get(r.Context(), int64(id))
	if err != nil {
		if errors.Is(err, persistence.ErrTaskNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "task not found"})
		} else {
			s.logger.Error("task_get_failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "task storage failed"})
		}
		return
	}
	record := httpRecord(stored)

	// Result 尚未到达时，允许查询 Running 状态。
	// 终态只由 Result Collector 写入，
	// 避免提前显示完成但结果尚未保存的情况。
	if record.State == "pending" {
		state, ok := s.runtime.State(id)

		if ok && state == runtimecore.StateRunning {
			record.State = "running"
		}
	}

	writeJSON(
		w,
		http.StatusOK,
		record,
	)
}

type metricsResponse struct {
	Completed int `json:"completed"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`

	Attempts int `json:"attempts"`
	Retries  int `json:"retries"`

	TotalAttemptDurationMS float64 `json:"total_attempt_duration_ms"`

	ErrorsByKind map[string]int `json:"errors_by_kind"`
}

func (s *Service) getMetrics(
	w http.ResponseWriter,
	r *http.Request,
) {
	snapshot := s.metrics.Snapshot()

	kinds := make(map[string]int)

	for kind, count := range snapshot.ErrorsByKind {
		kinds[fmt.Sprint(kind)] = count
	}

	writeJSON(
		w,
		http.StatusOK,
		metricsResponse{
			Completed: snapshot.Completed,
			Succeeded: snapshot.Succeeded,
			Failed:    snapshot.Failed,
			Cancelled: snapshot.Cancelled,

			Attempts: snapshot.Attempts,
			Retries:  snapshot.Retries,

			TotalAttemptDurationMS: float64(
				snapshot.TotalAttemptDuration.Microseconds(),
			) / 1000,

			ErrorsByKind: kinds,
		},
	)
}
