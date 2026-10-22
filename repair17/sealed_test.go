package repair17

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sealedFixture(t *testing.T, files map[string]string) Workspace {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if e := os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	for name, data := range files {
		q := filepath.Join(source, name)
		if e := os.MkdirAll(filepath.Dir(q), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(q, []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "sealed fixture"}} {
		if _, e := git(context.Background(), source, args...); e != nil {
			t.Fatal(e)
		}
	}
	w, e := Prepare(context.Background(), source, filepath.Join(root, "worktrees"), "sealed")
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func TestSealedVendoredDependencyIsCapturedAndOffline(t *testing.T) {
	w := sealedFixture(t, map[string]string{"go.mod": "module example.invalid/app\ngo 1.23\nrequire example.invalid/tiny v1.0.0\n", "main.go": "package app\nimport \"example.invalid/tiny\"\nfunc Answer()int{return tiny.Value}\n", "main_test.go": "package app\nimport \"testing\"\nfunc TestAnswer(t *testing.T){if Answer()!=42{t.Fatal(Answer())}}\n", "vendor/modules.txt": "# example.invalid/tiny v1.0.0\n## explicit; go 1.23\nexample.invalid/tiny\n", "vendor/example.invalid/tiny/value.go": "package tiny\nconst Value=42\n"})
	ctx := context.Background()
	v, e := CaptureVerification(ctx, w.Path, SealedVendor+":test", 20*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	if out, e := v.Verify(ctx, 20*time.Second); e != nil || !out.Passed {
		t.Fatal(out, e)
	}
	old := v.Digest
	if e = os.WriteFile(filepath.Join(w.Path, "vendor/example.invalid/tiny/value.go"), []byte("package tiny\nconst Value=0\n"), 0600); e != nil {
		t.Fatal(e)
	}
	now, e := VerificationDigest(ctx, w.Path, SealedVendor+":test", 20*time.Second)
	if e != nil || now == old {
		t.Fatal("dependency excluded from digest", e)
	}
	if out, e := v.Verify(ctx, 20*time.Second); e != nil || !out.Passed {
		t.Fatal("snapshot used ambient source", out, e)
	}
	if _, e = CaptureVerification(ctx, w.Path, "default", time.Second); e == nil {
		t.Fatal("default policy silently expanded")
	}
}
func TestSealedWorkspaceMembershipAndEscape(t *testing.T) {
	files := map[string]string{"go.work": "go 1.23\nuse (\n ./a\n ./b\n)\n", "a/go.mod": "module example.invalid/a\ngo 1.23\nrequire example.invalid/b v0.0.0\n", "a/main.go": "package a\nimport \"example.invalid/b\"\nfunc Answer()int{return b.Value}\n", "a/main_test.go": "package a\nimport \"testing\"\nfunc TestAnswer(t *testing.T){if Answer()!=42{t.Fatal(Answer())}}\n", "b/go.mod": "module example.invalid/b\ngo 1.23\n", "b/main.go": "package b\nconst Value=42\n"}
	w := sealedFixture(t, files)
	ctx := context.Background()
	v, e := CaptureVerification(ctx, w.Path, SealedWorkspace+":test", 20*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	if out, e := v.Verify(ctx, 20*time.Second); e != nil || !out.Passed {
		t.Fatal(out, e)
	}
	for _, work := range []string{"go 1.23\nuse ../outside\n", "go 1.23\nuse ./a\n"} {
		if e = os.WriteFile(filepath.Join(w.Path, "go.work"), []byte(work), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = CaptureVerification(ctx, w.Path, SealedWorkspace+":test", time.Second); e == nil {
			t.Fatal("escaped/unlisted input accepted")
		}
	}
}

func TestSealedWorkspaceDependencyAndManifestChangesInvalidateDigest(t *testing.T) {
	files := map[string]string{
		"go.work":        "go 1.23\nuse (\n ./a\n ./b\n)\n",
		"a/go.mod":       "module example.invalid/a\ngo 1.23\nrequire example.invalid/b v0.0.0\n",
		"a/main.go":      "package a\nimport \"example.invalid/b\"\nfunc Answer()int{return b.Value}\n",
		"a/main_test.go": "package a\nimport \"testing\"\nfunc TestAnswer(t *testing.T){if Answer()!=42{t.Fatal(Answer())}}\n",
		"b/go.mod":       "module example.invalid/b\ngo 1.23\n",
		"b/main.go":      "package b\nconst Value=42\n",
	}
	w := sealedFixture(t, files)
	ctx := context.Background()
	policy := SealedWorkspace + ":sandbox-fixed-policy-test"
	v, err := CaptureVerification(ctx, w.Path, policy, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	for _, change := range []struct{ name, value string }{
		{"b/main.go", "package b\nconst Value=0\n"},
		{"b/go.mod", "module example.invalid/b\ngo 1.24\n"},
		{"go.work", "go 1.24\nuse (\n ./a\n ./b\n)\n"},
	} {
		t.Run(change.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(w.Path, change.name), []byte(change.value), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := VerificationDigest(ctx, w.Path, policy, 20*time.Second)
			if err != nil || got == v.Digest {
				t.Fatal("changed dependency/manifest did not invalidate cached proof", err)
			}
			if err := os.WriteFile(filepath.Join(w.Path, change.name), []byte(files[change.name]), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	if result, err := v.Verify(ctx, 20*time.Second); err != nil || !result.Passed {
		t.Fatal("captured workspace was not isolated from later source changes", result, err)
	}
	if err := v.Unchanged(ctx); err != nil {
		t.Fatal("verification modified its captured input", err)
	}
}
