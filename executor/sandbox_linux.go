//go:build linux

package executor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime-app/internal/boundedlog"
	"runtime-app/task"
	"strings"
	"syscall"
	"time"
)

// GoSandbox runs a fixed command against a read-only candidate copy. All writable
// container storage is size-limited tmpfs. It never falls back to the host.
type GoSandbox struct {
	Image    string
	Root     string
	Timeout  time.Duration
	MaxBytes int64
	MaxFiles int
}

var imageDigest = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9./:_-]*@sha256:[0-9a-f]{64}$`)

func (s GoSandbox) Validate() error {
	if !imageDigest.MatchString(s.Image) || !filepath.IsAbs(s.Root) || s.Timeout <= 0 || s.Timeout > 5*time.Minute || s.MaxBytes < 1 || s.MaxBytes > 64<<20 || s.MaxFiles < 1 || s.MaxFiles > 10000 {
		return errors.New("invalid sandbox server policy")
	}
	return nil
}
func (s GoSandbox) Probe(ctx context.Context) error {
	if err := s.Validate(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", s.Image)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + os.Getenv("HOME")}
	if err := cmd.Run(); err != nil {
		return errors.New("pinned local sandbox image/engine unavailable; no host fallback")
	}
	return nil
}
func (s GoSandbox) arguments(name, path string) []string {
	return []string{"run", "--pull=never", "--name", name, "--label", "io.agent-runtime.sandbox=owned-v1", "--label", "io.agent-runtime.expires=" + fmt.Sprint(time.Now().Add(s.Timeout+time.Minute).Unix()),
		"--network=none", "--user=65532:65532", "--cap-drop=ALL", "--security-opt=no-new-privileges:true",
		"--read-only", "--cpus=1", "--memory=512m", "--memory-swap=512m", "--pids-limit=64", "--ulimit=nofile=128:128", "--ulimit=fsize=33554432:33554432",
		"--tmpfs=/tmp:rw,exec,nosuid,nodev,size=268435456,mode=1777",
		"--mount", "type=bind,src=" + path + ",dst=/src,readonly", "--workdir=/src",
		"--env=HOME=/tmp", "--env=GOCACHE=/tmp/cache", "--env=GOPATH=/tmp/gopath", "--env=GOWORK=off", "--env=GOTOOLCHAIN=local", "--env=GOPROXY=off", "--env=GOSUMDB=off", "--env=CGO_ENABLED=0",
		s.Image, "go", "test", "-count=1", "./..."}
}

// Snapshot rejects every symlink/hardlink/special file and limits total input.
// No archive extraction or user-defined mounts, images, environment or commands exist.
func (s GoSandbox) snapshot(source, target string) (string, error) {
	h := sha256.New()
	var total int64
	count := 0
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return errors.New("snapshot path escape")
		}
		if rel == "." {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("snapshot symlink refused")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.Mkdir(dst, 0755)
		}
		if !info.Mode().IsRegular() {
			return errors.New("snapshot special file refused")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return errors.New("snapshot hardlink refused")
		}
		count++
		total += info.Size()
		if count > s.MaxFiles || total > s.MaxBytes {
			return errors.New("snapshot exceeds policy")
		}
		// O_NOFOLLOW also rejects replacing an enumerated regular file with a symlink.
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		in := os.NewFile(uintptr(fd), path)
		defer in.Close()
		actual, err := in.Stat()
		if err != nil || !os.SameFile(info, actual) {
			return errors.New("snapshot source changed")
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0444)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(h, rel+"\x00")
		n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, s.MaxBytes-total+info.Size()+1))
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() {
			return errors.New("snapshot changed or exceeded limits")
		}
		_, _ = h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s GoSandbox) Execute(ctx context.Context, t task.Task) (Output, error) {
	out := Output{ExitCode: -1}
	if err := s.Validate(); err != nil {
		return out, err
	}
	if t.Command != "go-test" || len(t.Args) != 0 || !filepath.IsAbs(t.Payload) {
		return out, ErrCommandNotAllowed
	}
	root, err := filepath.EvalSymlinks(s.Root)
	if err != nil || root != s.Root {
		return out, errors.New("sandbox root must be an existing canonical directory")
	}
	dir, err := os.MkdirTemp(root, "candidate-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(dir)
	tree, err := s.snapshot(t.Payload, dir)
	if err != nil {
		return out, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return out, err
	}
	name := "agent-sandbox-" + hex.EncodeToString(nonce[:])
	runCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	// Label/name are generated locally. Worker SIGKILL needs the owned-container reaper.
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	}()
	cmd := exec.CommandContext(runCtx, "docker", s.arguments(name, dir)...)
	output := boundedlog.New(65536)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = time.Second
	started := time.Now()
	runErr := cmd.Run()
	inspectCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	inspected, inspectErr := exec.CommandContext(inspectCtx, "docker", "inspect", "--format={{json .State}}", name).Output()
	var state struct {
		Status    string
		ExitCode  int
		OOMKilled bool
	}
	if inspectErr != nil || json.Unmarshal(inspected, &state) != nil {
		return out, errors.New("sandbox supervisor unavailable; no verdict")
	}
	out.ExitCode = state.ExitCode
	out.Stdout = output.String()
	evidence, _ := json.Marshal(map[string]any{"candidate_tree_sha256": tree, "image": s.Image, "command": []string{"go", "test", "-count=1", "./..."}, "oom": state.OOMKilled, "duration_ms": time.Since(started).Milliseconds(), "supervisor_exit": state.ExitCode})
	out.Stderr = string(evidence)
	if runCtx.Err() != nil {
		return out, runCtx.Err()
	}
	if state.OOMKilled {
		return out, errors.New("sandbox OOM")
	}
	if state.Status != "exited" {
		return out, errors.New("sandbox did not exit")
	}
	if runErr != nil && state.ExitCode == 0 {
		return out, errors.New("sandbox CLI failure; verdict uncertain")
	}
	return out, nil
}
