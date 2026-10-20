//go:build linux

package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxPolicyAndSnapshotRejections(t *testing.T) {
	s := GoSandbox{Image: "golang@sha256:" + strings.Repeat("a", 64), Root: t.TempDir(), Timeout: time.Second, MaxBytes: 1000, MaxFiles: 10}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(s.arguments("owned", "/owned/candidate"), " ")
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
