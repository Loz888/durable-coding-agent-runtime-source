package repair18

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"runtime-app/repair17"
)

// 模型只能提出数据，不能直接写文件。
type SourceFile struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Source string `json:"source"`
}
type Change struct {
	File string `json:"file"`
	Old  string `json:"old"`
	New  string `json:"new"`
}
type Request struct {
	Files         []SourceFile `json:"files,omitempty"`
	ContextDigest string       `json:"context_digest,omitempty"`

	SourceCommit string
	SourceHash   string
	Round        int
	File         string
	Source       string
	TestOutput   string
	Feedback     string
}

type Suggestion struct {
	ProviderModel     string   `json:"provider_model,omitempty"`
	Changes           []Change `json:"changes,omitempty"`
	UsageKnown        bool     `json:"usage_known"`
	ProviderRequestID string   `json:"provider_request_id,omitempty"`
	Old               string   `json:"old"`
	New               string   `json:"new"`
	Reason            string   `json:"reason"`
	PromptTokens      int      `json:"-"`
	CompletionTokens  int      `json:"-"`
}

type Proposer interface {
	Suggest(
		context.Context,
		Request,
	) (Suggestion, error)
}

type Options struct {
	Worktree    string
	File        string
	MaxRounds   int
	TestTimeout time.Duration
}

type Step struct {
	Round        int    `json:"round"`
	Reason       string `json:"reason,omitempty"`
	BeforeSHA    string `json:"before_sha256,omitempty"`
	AfterSHA     string `json:"after_sha256,omitempty"`
	TestPassed   bool   `json:"test_passed"`
	TestExitCode int    `json:"test_exit_code"`
	TestOutput   string `json:"test_output,omitempty"`
	Error        string `json:"error,omitempty"`
}

type Result struct {
	RunID            string              `json:"run_id"`
	Worktree         string              `json:"worktree"`
	File             string              `json:"file"`
	Status           string              `json:"status"`
	Baseline         repair17.TestResult `json:"baseline"`
	LastTest         repair17.TestResult `json:"last_test"`
	Steps            []Step              `json:"steps"`
	Diff             string              `json:"diff,omitempty"`
	PromptTokens     int                 `json:"prompt_tokens"`
	CompletionTokens int                 `json:"completion_tokens"`
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}

func Run(
	ctx context.Context,
	model Proposer,
	opt Options,
) (Result, error) {
	var out Result

	if model == nil {
		return out, errors.New("nil proposer")
	}
	if opt.MaxRounds < 1 || opt.MaxRounds > 5 {
		return out, errors.New(
			"MaxRounds must be 1..5",
		)
	}
	if opt.TestTimeout <= 0 ||
		opt.TestTimeout > 5*time.Minute {
		return out, errors.New(
			"invalid test timeout",
		)
	}

	// 只接受 Day17 管理的 Worktree。
	w, err := repair17.OpenManaged(
		ctx, opt.Worktree,
	)
	if err != nil {
		return out, err
	}

	out = Result{
		RunID:    w.RunID,
		Worktree: w.Path,
		File:     opt.File,
	}

	// 沿用 Day17 的文件路径检查。
	if _, err := repair17.Inspect(
		ctx, w.Path, opt.File,
	); err != nil {
		return out, err
	}

	// 第一次真实执行 Go Test。
	baseline, err := repair17.VerifyGo(
		ctx, w.Path, opt.TestTimeout,
	)
	if err != nil {
		return out, fmt.Errorf(
			"baseline test harness: %w", err,
		)
	}

	out.Baseline = baseline
	out.LastTest = baseline

	// 测试已通过，不需要乱改源码。
	if baseline.Passed {
		out.Status = "already_passed"
		return out, nil
	}

	feedback := "Initial Go tests failed."

	for round := 1; round <= opt.MaxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}

		// 每轮重新检查最新文件版本。
		sha, err := repair17.Inspect(
			ctx, w.Path, opt.File,
		)
		if err != nil {
			return out, fmt.Errorf(
				"inspect before round %d: %w",
				round, err,
			)
		}

		source, err := os.ReadFile(
			filepath.Join(w.Path, opt.File),
		)
		if err != nil {
			return out, err
		}

		if len(source) > 16*1024 {
			return out, errors.New(
				"source exceeds 16 KiB Day18 prompt limit",
			)
		}

		// 模型只提出补丁建议。
		suggestion, err := model.Suggest(
			ctx,
			Request{
				Round:  round,
				File:   opt.File,
				Source: string(source),
				TestOutput: truncate(
					out.LastTest.Output, 8000,
				),
				Feedback: feedback,
			},
		)
		if err != nil {
			out.Status = "model_error"
			return out, fmt.Errorf(
				"model round %d: %w",
				round, err,
			)
		}

		out.PromptTokens += suggestion.PromptTokens
		out.CompletionTokens += suggestion.CompletionTokens

		step := Step{
			Round: round,
			Reason: truncate(
				suggestion.Reason, 500,
			),
		}

		// 拒绝空补丁、无变化补丁或过大补丁。
		if suggestion.Old == "" ||
			suggestion.Old == suggestion.New ||
			len(suggestion.Old) > 16*1024 ||
			len(suggestion.New) > 16*1024 {

			step.Error = "invalid suggested patch"
			out.Steps = append(out.Steps, step)
			out.Status = "rejected"
			return out, nil
		}

		// 实际修改仍交给 Day17。
		patched, err := repair17.Apply(
			ctx,
			w.Path,
			repair17.Patch{
				Path:           opt.File,
				ExpectedSHA256: sha,
				Old:            suggestion.Old,
				New:            suggestion.New,
			},
		)

		if err != nil {
			step.Error = "patch rejected: " +
				truncate(err.Error(), 300)

			out.Steps = append(out.Steps, step)
			out.Status = "rejected"
			return out, nil
		}

		step.BeforeSHA = patched.BeforeSHA256
		step.AfterSHA = patched.AfterSHA256

		// 运行真实 Go 测试，而不是询问模型
		// “你觉得是否修好了？”
		test, err := repair17.VerifyGo(
			ctx, w.Path, opt.TestTimeout,
		)

		if err != nil {
			step.Error = "test harness: " +
				err.Error()

			out.Steps = append(out.Steps, step)
			out.Status = "verify_error"
			return out, err
		}

		out.LastTest = test
		step.TestPassed = test.Passed
		step.TestExitCode = test.ExitCode
		step.TestOutput = truncate(
			test.Output, 4000,
		)

		out.Steps = append(out.Steps, step)

		if test.Passed {
			out.Status = "repaired"
			break
		}

		// 保留修改，让模型基于当前源码继续诊断。
		feedback = fmt.Sprintf(
			"Round %d patch applied, but go test still failed. "+
				"Repair the CURRENT file using the latest failure log.",
			round,
		)
	}

	if out.Status == "" {
		out.Status = "exhausted"
	}

	diff, err := repair17.Diff(
		ctx, w.Path,
	)
	if err != nil {
		return out, err
	}

	out.Diff = diff
	return out, nil
}

// 确定性模拟器，不是真实模型。
// 仅用于 divide-assert 演示。
type ScriptedFix struct{}

func (ScriptedFix) Suggest(
	_ context.Context,
	r Request,
) (Suggestion, error) {
	if strings.Contains(
		r.Source, "return a * b",
	) {
		return Suggestion{
			Old:    "return a * b",
			New:    "return a / b",
			Reason: "MOCK: deterministic divide fix",
		}, nil
	}

	return Suggestion{},
		errors.New(
			"MOCK only handles the divide fixture",
		)
}
