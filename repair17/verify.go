package repair17

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime-app/internal/boundedlog"
	"time"
)

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

	return runGoTests(ctx, w.Path, timeout)
}

func runGoTests(ctx context.Context, directory string, timeout time.Duration) (TestResult, error) {
	return runTestCommand(ctx, directory, timeout, []string{"test", "-count=1", "./..."}, trustedGoTestEnv())
}
func runTestCommand(ctx context.Context, directory string, timeout time.Duration, args, env []string) (TestResult, error) {
	var result TestResult
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
		args...,
	)

	cmd.Dir = directory

	cmd.Env = env

	cmd.WaitDelay = 2 * time.Second

	output := boundedlog.New(64 * 1024)

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
		"GOENV=off",
		"GOFLAGS=",
		"CGO_ENABLED=0",
		"GOOS="+runtime.GOOS,
		"GOARCH="+runtime.GOARCH,
	)
}
