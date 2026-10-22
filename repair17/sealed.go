package repair17

import (
	"context"
	"errors"
	"path/filepath"
	"runtime-app/internal/goinput"
	"strings"
	"time"
)

const SealedVendor = goinput.SealedVendor
const SealedWorkspace = goinput.SealedWorkspace

func inputMode(policy string) string      { return goinput.Mode(policy) }
func ValidInputPolicy(policy string) bool { return goinput.ValidPolicy(policy) }
func sealedCommand(ctx context.Context, path, policy string) ([]string, error) {
	return goinput.TestCommand(ctx, path, inputMode(policy))
}
func verificationEnv(path, policy string) []string {
	env := trustedGoTestEnv()
	if inputMode(policy) == SealedWorkspace {
		for i, e := range env {
			if strings.HasPrefix(e, "GOWORK=") {
				env[i] = "GOWORK=" + filepath.Join(path, "go.work")
			}
		}
	}
	return env
}
func VerifyWithPolicy(ctx context.Context, path, inputPolicy string, timeout time.Duration) (TestResult, error) {
	if !ValidInputPolicy(inputPolicy) {
		return TestResult{}, errors.New("unknown sealed input policy")
	}
	v, e := CaptureVerification(ctx, path, inputPolicy+":trusted-local-v1", timeout)
	if e != nil {
		return TestResult{}, e
	}
	defer v.Close()
	out, e := v.Verify(ctx, timeout)
	if e == nil {
		e = v.Unchanged(ctx)
	}
	return out, e
}
