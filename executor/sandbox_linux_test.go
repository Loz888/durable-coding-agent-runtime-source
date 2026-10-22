//go:build linux

package executor

import (
	"context"
	"os"
	"path/filepath"
	"runtime-app/internal/goinput"
	"runtime-app/task"
	"strings"
	"testing"
	"time"
)

func TestSandboxPolicyAndSnapshotRejections(t *testing.T) {
	s := GoSandbox{Image: "golang@sha256:" + strings.Repeat("a", 64), Root: t.TempDir(), Timeout: time.Second, MaxBytes: 1000, MaxFiles: 10}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(s.arguments("owned", "/owned/candidate", []string{"test", "-count=1", "./..."}), " ")
	for _, required := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--pids-limit=64", "--memory-swap=512m", "--tmpfs=/tmp:", "readonly", "--pull=never", "--user=65532:65532"} {
		if !strings.Contains(args, required) {
			t.Fatal("missing policy", required)
		}
	}
	for _, forbidden := range []string{"--privileged", "docker.sock", "--network=host", "--pid=host", "DEEPSEEK", "DATABASE_URL"} {
		if strings.Contains(args, forbidden) {
			t.Fatal("unsafe policy", forbidden)
		}
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "safe.go"), []byte("package safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if hash, err := s.snapshot(source, t.TempDir()); err != nil || len(hash) != 64 {
		t.Fatal(hash, err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.snapshot(source, t.TempDir()); err == nil {
		t.Fatal("symlink accepted")
	}
	_ = os.Remove(filepath.Join(source, "escape"))
	if err := os.Link(filepath.Join(source, "safe.go"), filepath.Join(source, "hard")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.snapshot(source, t.TempDir()); err == nil {
		t.Fatal("hardlink accepted")
	}
	_ = os.Remove(filepath.Join(source, "hard"))
	s.MaxBytes = 1
	if _, err := s.snapshot(source, t.TempDir()); err == nil {
		t.Fatal("oversize accepted")
	}
	s.Image = "golang:latest"
	if s.Validate() == nil {
		t.Fatal("mutable image accepted")
	}
}

// This filesystem regression is not a real container-isolation acceptance test.
func TestSandboxSnapshotTraversableForUnprivilegedContainer(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	if e := os.Chmod(target, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(source, "safe.go"), []byte("package safe"), 0600); e != nil {
		t.Fatal(e)
	}
	s := GoSandbox{MaxBytes: 1024, MaxFiles: 10}
	if _, e := s.snapshot(source, target); e != nil {
		t.Fatal(e)
	}
	root, e := os.Stat(target)
	if e != nil {
		t.Fatal(e)
	}
	if root.Mode().Perm()&0005 != 0005 {
		t.Fatal("snapshot root cannot be traversed/read by container UID65532")
	}
	file, e := os.Stat(filepath.Join(target, "safe.go"))
	if e != nil || file.Mode().Perm()&0004 == 0 {
		t.Fatal("snapshot file unreadable")
	}
	original, e := os.Stat(filepath.Join(source, "safe.go"))
	if e != nil || original.Mode().Perm() != 0600 {
		t.Fatal("original permissions changed")
	}
}

func TestSandboxSealedPoliciesRetainConfinement(t *testing.T) {
	for _, policy := range []string{goinput.SealedVendor, goinput.SealedWorkspace} {
		s := GoSandbox{InputPolicy: policy, Image: "golang@sha256:" + strings.Repeat("a", 64), Root: t.TempDir(), Timeout: time.Second, MaxBytes: 1024, MaxFiles: 10}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		args := strings.Join(s.arguments("owned", "/owned/candidate", []string{"test", "-count=1", "./a/..."}), " ")
		for _, required := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--user=65532:65532", "--env=GOPROXY=off", "--env=GOTOOLCHAIN=local", "--env=GOENV=off", "--env=GOFLAGS="} {
			if !strings.Contains(args, required) {
				t.Fatal(policy, "lost restriction", required)
			}
		}
		expected := "--env=GOWORK=off"
		if policy == goinput.SealedWorkspace {
			expected = "--env=GOWORK=/src/go.work"
		}
		if !strings.Contains(args, expected) {
			t.Fatal("wrong workspace environment", args)
		}
	}
}

func TestSandboxRejectsSealedContractBeforeStartingDocker(t *testing.T) {
	toolDir := t.TempDir()
	marker := filepath.Join(toolDir, "docker-called")
	script := "#!/bin/sh\nprintf called > '" + marker + "'\nexit 97\n"
	if err := os.WriteFile(filepath.Join(toolDir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cases := []struct{ name, policy, mod, work string }{
		{"default-external", "", "module example.invalid/a\ngo 1.23\nrequire example.invalid/b v1.0.0\n", ""},
		{"vendor-replace", goinput.SealedVendor, "module example.invalid/a\ngo 1.23\nreplace example.invalid/b => ../outside\n", ""},
		{"workspace-escape", goinput.SealedWorkspace, "module example.invalid/a\ngo 1.23\n", "go 1.23\nuse ../outside\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte(c.mod), 0600); err != nil {
				t.Fatal(err)
			}
			if c.work != "" {
				if err := os.WriteFile(filepath.Join(source, "go.work"), []byte(c.work), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s := GoSandbox{InputPolicy: c.policy, Image: "golang@sha256:" + strings.Repeat("a", 64), Root: t.TempDir(), Timeout: time.Second, MaxBytes: 4096, MaxFiles: 20}
			if _, err := s.Execute(context.Background(), task.Task{Command: "go-test", Payload: source}); err == nil {
				t.Fatal("invalid input accepted")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("Docker reached before contract rejection", err)
			}
			entries, err := os.ReadDir(s.Root)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed snapshot not cleaned", err)
			}
		})
	}
	s := GoSandbox{InputPolicy: "unsupported", Image: "golang@sha256:" + strings.Repeat("a", 64), Root: t.TempDir(), Timeout: time.Second, MaxBytes: 1024, MaxFiles: 10}
	if s.Validate() == nil {
		t.Fatal("unknown policy accepted")
	}
}
