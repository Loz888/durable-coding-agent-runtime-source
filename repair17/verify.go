package repair17

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Bounded test output, maximum 64 KiB.
type boundedOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	cap       int
	truncated bool
}

func (w *boundedOutput) Write(
	p []byte,
) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := len(p)
	room := w.cap - w.buf.Len()

	if room < 0 {
		room = 0
	}

	if len(p) > room {
		_, _ = w.buf.Write(p[:room])
		w.truncated = true
	} else {
		_, _ = w.buf.Write(p)
	}

	return n, nil
}

func (w *boundedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	s := w.buf.String()

	if w.truncated {
		s += "\n...[output truncated at 64 KiB]"
	}

	return s
}

type TestResult struct {
	Passed     bool   `json:"passed"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
}

// Runs a fixed Go test command.
// Only use trusted local repositories.
func VerifyGo(
	ctx context.Context,
	worktree string,
	timeout time.Duration,
) (TestResult, error) {
	var result TestResult

	w, err := OpenManaged(ctx, worktree)
	if err != nil {
		return result, err
	}

	if timeout <= 0 ||
		timeout > 5*time.Minute {
		return result, fmt.Errorf(
			"timeout must be >0 and <=5m",
		)
	}

	testCtx, cancel := context.WithTimeout(
		ctx, timeout,
	)
	defer cancel()

	cmd := exec.CommandContext(
		testCtx,
		"go",
		"test",
		"-count=1",
		"./...",
	)

	cmd.Dir = w.Path

	cmd.Env = trustedGoTestEnv()

	cmd.WaitDelay = 2 * time.Second

	output := &boundedOutput{
		cap: 64 * 1024,
	}

	cmd.Stdout = output
	cmd.Stderr = output

	started := time.Now()
	runErr := cmd.Run()

	result.DurationMS = time.Since(
		started,
	).Milliseconds()

	result.Output = output.String()
	result.ExitCode = -1

	if cmd.ProcessState != nil {
		result.ExitCode =
			cmd.ProcessState.ExitCode()
	}

	result.Passed = runErr == nil

	if testCtx.Err() != nil {
		return result, testCtx.Err()
	}

	if runErr != nil &&
		cmd.ProcessState == nil {
		return result, runErr
	}

	// A failing Go test is evidence,
	// not necessarily a harness error.
	return result, nil
}

// Only forward the environment needed by the Go toolchain.
// This is NOT a security sandbox.
func trustedGoTestEnv() []string {
	keys := []string{
		"PATH",
		"HOME",
		"TMPDIR",
		"GOCACHE",
		"GOPATH",
		"GOROOT",
		"LANG",
		"LC_ALL",
		"TZ",
		"USER",
	}

	env := make([]string, 0, len(keys)+4)

	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(
				env,
				key+"="+value,
			)
		}
	}

	return append(
		env,
		"GOWORK=off",
		"GOTOOLCHAIN=local",
		"GOPROXY=off",
		"GOSUMDB=off",
	)
}
