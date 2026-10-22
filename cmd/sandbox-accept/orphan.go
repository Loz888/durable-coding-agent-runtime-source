package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/executor"
	"runtime-app/task"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func orphanChild(image, root, source string) int {
	policy := executor.GoSandbox{Image: image, Root: root, Timeout: 8 * time.Second, CleanupGrace: time.Second, MaxBytes: 1 << 20, MaxFiles: 50}
	_, e := policy.Execute(context.Background(), task.Task{Command: "go-test", Payload: source})
	if e != nil {
		return 1
	}
	return 0
}
func orphanAcceptance(ctx context.Context, image, root string) error {
	snapshots := filepath.Join(root, "orphan-snapshots")
	if e := os.Mkdir(snapshots, 0700); e != nil {
		return e
	}
	source := filepath.Join(root, "orphan-source")
	if e := os.Mkdir(source, 0700); e != nil {
		return e
	}
	for name, text := range map[string]string{"go.mod": "module example.invalid/orphan\n\ngo 1.23\n", "wait_test.go": "package fixture\nimport(\"testing\";\"time\")\nfunc TestWait(t *testing.T){time.Sleep(30*time.Second)}\n"} {
		if e := os.WriteFile(filepath.Join(source, name), []byte(text), 0600); e != nil {
			return e
		}
	}
	binary, e := os.Executable()
	if e != nil {
		return e
	}
	child := exec.CommandContext(ctx, binary, "-orphan-child", "-image", image, "-child-root", snapshots, "-child-source", source)
	if e = child.Start(); e != nil {
		return e
	}
	waited := false
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	list := func() ([]string, error) {
		raw, e := exec.CommandContext(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=io.agent-runtime.sandbox=owned-v1", "--filter", "label=io.agent-runtime.root="+executor.HashRoot(snapshots)).Output()
		return strings.Fields(string(raw)), e
	}
	var ids []string
	readyBy := time.Now().Add(5 * time.Second)
	for time.Now().Before(readyBy) {
		ids, e = list()
		if e != nil {
			return errors.New("cannot inspect orphan fixture")
		}
		if len(ids) == 1 {
			state, stateErr := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", ids[0]).Output()
			if stateErr == nil && strings.TrimSpace(string(state)) == "true" {
				break
			}
			ids = nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	if len(ids) != 1 {
		return errors.New("worker did not create exact owned container")
	}
	id := ids[0]
	defer exec.Command("docker", "rm", "-f", id).Run()
	if e = child.Process.Kill(); e != nil {
		return errors.New("worker kill failed")
	}
	e = child.Wait()
	waited = true
	var killed *exec.ExitError
	if !errors.As(e, &killed) {
		return errors.New("worker did not terminate")
	}
	status, ok := killed.Sys().(syscall.WaitStatus)
	if !ok || status.Signal() != syscall.SIGKILL {
		return errors.New("worker was not SIGKILLed")
	}
	raw, e := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{json .Config.Labels}}`, id).Output()
	if e != nil {
		return errors.New("orphan disappeared without supervisor")
	}
	var labels map[string]string
	if json.Unmarshal(raw, &labels) != nil {
		return errors.New("invalid orphan labels")
	}
	// This real wall-clock deadline comes from the immutable container label.
	var expires int64
	if expires, e = strconv.ParseInt(labels["io.agent-runtime.expires"], 10, 64); e != nil {
		return errors.New("invalid orphan deadline")
	}
	policy := executor.GoSandbox{Image: image, Root: snapshots, Timeout: 8 * time.Second, CleanupGrace: time.Second, MaxBytes: 1 << 20, MaxFiles: 50}
	before, e := policy.ReapExpired(ctx)
	if e != nil || before.Removed != 0 || before.Retained != 1 {
		return errors.New("supervisor removed an unexpired owned container")
	}
	// A separate owned sentinel deliberately has another root identity. It must
	// survive even though its expiration is in the past. Cleanup is explicit.
	var nonce [8]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return e
	}
	sentinel := "agent-orphan-sentinel-" + hex.EncodeToString(nonce[:])
	args := []string{"run", "-d", "--name", sentinel, "--network=none", "--user=65532:65532", "--cap-drop=ALL", "--read-only", "--security-opt=no-new-privileges:true", "--memory=32m", "--pids-limit=16", "--label", "io.agent-runtime.sandbox=owned-v1", "--label", "io.agent-runtime.root=" + executor.HashRoot(filepath.Join(root, "different-root")), "--label", "io.agent-runtime.expires=1", image, "sleep", "30"}
	if e = exec.CommandContext(ctx, "docker", args...).Run(); e != nil {
		return errors.New("sentinel fixture unavailable")
	}
	defer exec.Command("docker", "rm", "-f", sentinel).Run()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Unix() <= expires {
		if time.Now().After(deadline) {
			return errors.New("actual orphan deadline exceeded")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	result, e := policy.ReapExpired(ctx)
	if e != nil || result.Removed != 1 || result.SnapshotsRemoved != 1 {
		return errors.New("expired orphan container/snapshot not reclaimed")
	}
	left, e := list()
	if e != nil || len(left) != 0 {
		return errors.New("owned orphan remained")
	}
	raw, e = exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", sentinel).Output()
	if e != nil || strings.TrimSpace(string(raw)) != "true" {
		return errors.New("different-root sentinel was touched")
	}
	entries, e := os.ReadDir(snapshots)
	if e != nil || len(entries) != 0 {
		return errors.New("orphan candidate files remained")
	}
	return nil
}
