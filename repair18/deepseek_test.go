package repair18

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime-app/internal/providererr"
	"testing"
)

func TestDay18DeepSeekJSONResponse(t *testing.T) {
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" ||
					r.Header.Get("Authorization") != "Bearer dummy" {
					t.Error("request invalid")
				}

				w.Header().Set(
					"Content-Type",
					"application/json",
				)

				fmt.Fprint(
					w,
					`{"choices":[{"finish_reason":"stop","message":{"content":"{\"old\":\"a * b\",\"new\":\"a / b\",\"reason\":\"修复错误\"}"}}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`,
				)
			},
		),
	)
	defer server.Close()

	model := DeepSeek{
		APIKey:   "dummy",
		Endpoint: server.URL,
	}

	result, err := model.Suggest(
		context.Background(),
		Request{
			Round:  1,
			File:   "math.go",
			Source: "a * b",
		},
	)

	if err != nil ||
		result.New != "a / b" ||
		result.PromptTokens != 100 ||
		result.CompletionTokens != 20 {
		t.Fatalf(
			"%+v %v",
			result, err,
		)
	}
}

func TestDay18DeepSeekRejectBadResponse(
	t *testing.T,
) {
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(
					w,
					`{"choices":[{"finish_reason":"length","message":{"content":"{}"}}]}`,
				)
			},
		),
	)
	defer server.Close()

	model := DeepSeek{
		APIKey:   "dummy",
		Endpoint: server.URL,
	}

	_, err := model.Suggest(
		context.Background(),
		Request{Round: 1},
	)

	var failure providererr.Failure
	if !errors.As(err, &failure) || failure.Category() != providererr.Truncated || failure.Temporary() || !failure.MayHaveCharged() {
		t.Fatalf("wrong truncated failure contract: %v", err)
	}

}
