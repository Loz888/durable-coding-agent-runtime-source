package repodiag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// 创建一个专门用于 DeepSeek 测试的 Go 仓库。
func deepSeekTestRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	files := map[string]string{
		"go.mod": `module example.com/deepseek-test

go 1.22
`,

		"math.go": `package mathtest

func Add(a, b int) int {
	return a + b
}
`,

		"math_test.go": `package mathtest

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("unexpected result")
	}
}
`,
	}

	for name, content := range files {

		path := filepath.Join(dir, name)

		err := os.WriteFile(
			path,
			[]byte(content),
			0600,
		)

		if err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

// Test 1：
// 验证模型在一轮输出两个 ToolCall 时，
// 下一轮请求能正确包含两个调用和两个结果。
func TestDay11DeepSeekMultipleToolCalls(
	t *testing.T,
) {

	var requestCount atomic.Int32

	server := httptest.NewServer(
		http.HandlerFunc(
			func(
				w http.ResponseWriter,
				r *http.Request,
			) {

				if r.URL.Path != "/chat/completions" {
					t.Errorf(
						"unexpected path: %s",
						r.URL.Path,
					)
				}

				if r.Header.Get(
					"Authorization",
				) != "Bearer test-key" {
					t.Error(
						"authorization header incorrect",
					)
				}

				var body struct {
					Model string `json:"model"`

					Thinking struct {
						Type string `json:"type"`
					} `json:"thinking"`

					Tools []json.RawMessage `json:"tools"`

					Messages []struct {
						Role string `json:"role"`

						ToolCalls []struct {
							ID string `json:"id"`
						} `json:"tool_calls"`

						ToolCallID string `json:"tool_call_id"`
					} `json:"messages"`
				}

				if err := json.NewDecoder(
					r.Body,
				).Decode(&body); err != nil {
					t.Error(err)
					http.Error(
						w,
						"bad request",
						http.StatusBadRequest,
					)
					return
				}

				if body.Model != "deepseek-flash" {
					t.Errorf(
						"model=%s",
						body.Model,
					)
				}

				if body.Thinking.Type != "disabled" {
					t.Errorf(
						"thinking=%s",
						body.Thinking.Type,
					)
				}

				if len(body.Tools) != 3 {
					t.Errorf(
						"tools=%d, want 3",
						len(body.Tools),
					)
				}

				n := requestCount.Add(1)

				w.Header().Set(
					"Content-Type",
					"application/json",
				)

				if n == 1 {

					// 第一轮要求调用两个真实工具。
					_, _ = w.Write(
						[]byte(`{
							"choices": [{
								"finish_reason": "tool_calls",
								"message": {
									"role": "assistant",
									"content": null,
									"tool_calls": [
										{
											"id": "call-list",
											"type": "function",
											"function": {
												"name": "list_files",
												"arguments": "{}"
											}
										},
										{
											"id": "call-read",
											"type": "function",
											"function": {
												"name": "read_file",
												"arguments": "{\"path\":\"math.go\"}"
											}
										}
									]
								}
							}]
						}`),
					)

					return
				}

				if n == 2 {

					// 第一条是 system，第二条是 user。
					// 第三条是 assistant 的 ToolCalls。
					// 第四、五条是对应的 ToolResult。
					if len(body.Messages) != 5 {
						t.Errorf(
							"messages=%d, want 5",
							len(body.Messages),
						)
					} else {

						assistant := body.Messages[2]

						if assistant.Role != "assistant" ||
							len(assistant.ToolCalls) != 2 {

							t.Error(
								"assistant tool calls not preserved",
							)
						}

						if body.Messages[3].Role != "tool" ||
							body.Messages[3].ToolCallID != "call-list" {

							t.Error(
								"first tool result mismatched",
							)
						}

						if body.Messages[4].Role != "tool" ||
							body.Messages[4].ToolCallID != "call-read" {

							t.Error(
								"second tool result mismatched",
							)
						}
					}

					_, _ = w.Write(
						[]byte(`{
							"choices": [{
								"finish_reason": "stop",
								"message": {
									"role": "assistant",
									"content": "诊断完成：已经读取仓库文件。"
								}
							}]
						}`),
					)

					return
				}

				t.Error(
					"unexpected third request",
				)

				http.Error(
					w,
					"unexpected request",
					http.StatusBadRequest,
				)
			},
		),
	)

	defer server.Close()

	workspace, err := NewWorkspace(
		deepSeekTestRepo(t),
	)

	if err != nil {
		t.Fatal(err)
	}

	agent := Agent{
		Model: DeepSeekModel{
			APIKey: "test-key",
			Model:  "deepseek-flash",

			Endpoint: server.URL +
				"/chat/completions",

			Client: server.Client(),
		},

		Workspace: workspace,
		MaxTurns:  4,
		MaxCalls:  4,
	}

	report, err := agent.Run(
		context.Background(),
		"检查Go仓库",
	)

	if err != nil {
		t.Fatal(err)
	}

	if report.Status != "succeeded" {
		t.Fatalf(
			"status=%s",
			report.Status,
		)
	}

	if len(report.Steps) != 2 {
		t.Fatalf(
			"steps=%d, want 2",
			len(report.Steps),
		)
	}

	if report.Steps[0].Name != "list_files" {
		t.Fatal(
			"first tool should be list_files",
		)
	}

	if report.Steps[1].Name != "read_file" {
		t.Fatal(
			"second tool should be read_file",
		)
	}

	if report.Steps[0].IsError ||
		report.Steps[1].IsError {

		t.Fatalf(
			"unexpected tool error: %+v",
			report.Steps,
		)
	}

	if !strings.Contains(
		report.Final,
		"诊断完成",
	) {
		t.Fatalf(
			"unexpected final report: %s",
			report.Final,
		)
	}

	if requestCount.Load() != 2 {
		t.Fatalf(
			"requests=%d, want 2",
			requestCount.Load(),
		)
	}
}

// Test 2：API 失败不能被伪装为成功。
func TestDay11DeepSeekHTTPError(t *testing.T) {

	server := httptest.NewServer(
		http.HandlerFunc(
			func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				http.Error(
					w,
					`{"error":{"message":"rate limit"}}`,
					http.StatusTooManyRequests,
				)
			},
		),
	)

	defer server.Close()

	model := DeepSeekModel{
		APIKey:   "test-key",
		Model:    "deepseek-flash",
		Endpoint: server.URL,
		Client:   server.Client(),
	}

	history := []json.RawMessage{
		json.RawMessage(
			`{"role":"user","content":"test"}`,
		),
	}

	_, err := model.Next(
		context.Background(),
		history,
	)

	if err == nil ||
		!strings.Contains(err.Error(), "429") {
		t.Fatalf(
			"expected HTTP 429, got %v",
			err,
		)
	}
}

// Test 3：取消的 Context 不应继续调用模型。
func TestDay11DeepSeekCancelledContext(
	t *testing.T,
) {

	var calls atomic.Int32

	server := httptest.NewServer(
		http.HandlerFunc(
			func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				calls.Add(1)
				w.WriteHeader(http.StatusOK)
			},
		),
	)

	defer server.Close()

	model := DeepSeekModel{
		APIKey:   "test-key",
		Model:    "deepseek-flash",
		Endpoint: server.URL,
		Client:   server.Client(),
	}

	ctx, cancel := context.WithCancel(
		context.Background(),
	)

	cancel()

	_, err := model.Next(
		ctx,
		[]json.RawMessage{
			json.RawMessage(
				`{"role":"user","content":"hello"}`,
			),
		},
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"expected context.Canceled, got %v",
			err,
		)
	}

	if calls.Load() != 0 {
		t.Fatal(
			"cancelled request reached server",
		)
	}
}

// Test 4：模型返回空 Choices，应当报错。
func TestDay11DeepSeekEmptyChoices(
	t *testing.T,
) {

	server := httptest.NewServer(
		http.HandlerFunc(
			func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				w.Header().Set(
					"Content-Type",
					"application/json",
				)

				_, _ = w.Write(
					[]byte(`{"choices":[]}`),
				)
			},
		),
	)

	defer server.Close()

	model := DeepSeekModel{
		APIKey:   "test-key",
		Model:    "deepseek-flash",
		Endpoint: server.URL,
		Client:   server.Client(),
	}

	_, err := model.Next(
		context.Background(),
		[]json.RawMessage{
			json.RawMessage(
				`{"role":"user","content":"hello"}`,
			),
		},
	)

	if err == nil {
		t.Fatal(
			"empty choices should return error",
		)
	}
}

// Test 5：模型输出被截断时不应产生有效 ToolCall。
func TestDay11DeepSeekTruncatedResponse(
	t *testing.T,
) {

	server := httptest.NewServer(
		http.HandlerFunc(
			func(
				w http.ResponseWriter,
				r *http.Request,
			) {
				w.Header().Set(
					"Content-Type",
					"application/json",
				)

				_, _ = w.Write(
					[]byte(`{
						"choices": [{
							"finish_reason": "length",
							"message": {
								"role": "assistant",
								"content": "partial answer"
							}
						}]
					}`),
				)
			},
		),
	)

	defer server.Close()

	model := DeepSeekModel{
		APIKey:   "test-key",
		Model:    "deepseek-flash",
		Endpoint: server.URL,
		Client:   server.Client(),
	}

	_, err := model.Next(
		context.Background(),
		[]json.RawMessage{
			json.RawMessage(
				`{"role":"user","content":"hello"}`,
			),
		},
	)

	if err == nil ||
		!strings.Contains(
			err.Error(),
			"truncated",
		) {
		t.Fatalf(
			"expected truncation error, got %v",
			err,
		)
	}
}
