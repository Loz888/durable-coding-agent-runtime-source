package repair17

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitTest(
	t *testing.T,
	dir string,
	args ...string,
) {
	t.Helper()

	cmd := exec.Command(
		"git",
		append([]string{"-C", dir}, args...)...,
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf(
			"git %v: %v, output=%s",
			args, err, out,
		)
	}
}

// Creates a small independent Git repository.
func fixture(
	t *testing.T,
	bad bool,
) (string, string) {
	t.Helper()

	root := t.TempDir()

	src := filepath.Join(root, "source")
	base := filepath.Join(root, "worktrees")

	if err := os.Mkdir(src, 0755); err != nil {
		t.Fatal(err)
	}

	for name, content := range map[string]string{
		"go.mod": "module example.com/repair17fixture\n\ngo 1.23\n",

		"math.go": `package example
func Divide(a, b int) int { return a * b }
`,

		"math_test.go": `package example
import "testing"
func TestDivide(t *testing.T) {
    if got := Divide(12, 3); got != 4 {
        t.Fatalf("got %d", got)
    }
}
`,
	} {
		if !bad && name == "math.go" {
			content = `package example
func Divide(a, b int) int { return a / b }
`
		}

		if err := os.WriteFile(
			filepath.Join(src, name),
			[]byte(content),
			0644,
		); err != nil {
			t.Fatal(err)
		}
	}

	gitTest(t, src, "init", "-q")
	gitTest(t, src, "add", ".")

	gitTest(
		t, src,
		"-c", "user.name=Day17 Test",
		"-c", "user.email=day17@example.invalid",
		"commit", "-qm", "fixture",
	)

	return src, base
}

func TestDay17PrepareIsolation(
	t *testing.T,
) {
	src, base := fixture(t, true)

	ctx := context.Background()

	w, err := Prepare(
		ctx, src, base, "run-1",
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := OpenManaged(
		ctx, w.Path,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := Prepare(
		ctx, src, base, "run-1",
	); err == nil {
		t.Fatal("duplicate run accepted")
	}

	if _, err := Prepare(
		ctx,
		src,
		filepath.Join(src, "inside"),
		"run-2",
	); err == nil {
		t.Fatal("nested worktree accepted")
	}

	if _, err := Prepare(
		ctx, src, base, "../bad",
	); err == nil {
		t.Fatal("invalid run ID accepted")
	}

	sourceBytes, err := os.ReadFile(
		filepath.Join(src, "math.go"),
	)

	if err != nil ||
		!strings.Contains(
			string(sourceBytes),
			"a * b",
		) {
		t.Fatal("source changed")
	}
}

func TestDay17ApplyCASAndReplay(
	t *testing.T,
) {
	src, base := fixture(t, true)

	ctx := context.Background()

	w, err := Prepare(
		ctx, src, base, "repair",
	)
	if err != nil {
		t.Fatal(err)
	}

	sha, err := Inspect(
		ctx, w.Path, "math.go",
	)
	if err != nil {
		t.Fatal(err)
	}

	p := Patch{
		Path:           "math.go",
		ExpectedSHA256: sha,
		Old:            "return a * b",
		New:            "return a / b",
	}

	result, err := Apply(
		ctx, w.Path, p,
	)

	if err != nil ||
		result.BeforeSHA256 != sha ||
		result.AfterSHA256 == sha {
		t.Fatalf(
			"apply=%+v err=%v",
			result, err,
		)
	}

	// A stale replay MUST be rejected.
	if _, err := Apply(
		ctx, w.Path, p,
	); err == nil {
		t.Fatal("stale replay was accepted")
	}

	tr, err := VerifyGo(
		ctx,
		w.Path,
		15*time.Second,
	)

	if err != nil || !tr.Passed {
		t.Fatalf(
			"verify=%+v err=%v",
			tr, err,
		)
	}

	diff, err := Diff(ctx, w.Path)

	if err != nil ||
		!strings.Contains(
			diff,
			"return a / b",
		) {
		t.Fatalf(
			"diff=%q err=%v",
			diff, err,
		)
	}

	original, err := os.ReadFile(
		filepath.Join(src, "math.go"),
	)

	if err != nil ||
		!strings.Contains(
			string(original),
			"return a * b",
		) {
		t.Fatal("original source was modified")
	}
}

func TestDay17RejectPathTraversalAndTestModification(
	t *testing.T,
) {
	src, base := fixture(t, true)

	ctx := context.Background()

	w, err := Prepare(
		ctx, src, base, "reject",
	)
	if err != nil {
		t.Fatal(err)
	}

	sha, _ := Inspect(
		ctx, w.Path, "math.go",
	)

	for _, name := range []string{
		"../math.go",
		".git/config",
		"math_test.go",
		"go.mod",
		"/etc/passwd",
	} {
		_, err := Apply(
			ctx,
			w.Path,
			Patch{
				Path:           name,
				ExpectedSHA256: sha,
				Old:            "x",
				New:            "y",
			},
		)

		if err == nil {
			t.Fatalf(
				"unsafe path accepted: %s",
				name,
			)
		}
	}

	if err := os.Symlink(
		"math.go",
		filepath.Join(w.Path, "alias.go"),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := Inspect(
		ctx, w.Path, "alias.go",
	); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestDay17RejectAmbiguousAndStalePatch(
	t *testing.T,
) {
	src, base := fixture(t, true)

	ctx := context.Background()

	w, err := Prepare(
		ctx, src, base, "ambiguous",
	)
	if err != nil {
		t.Fatal(err)
	}

	sha, _ := Inspect(
		ctx, w.Path, "math.go",
	)

	cases := []Patch{
		{
			Path:           "math.go",
			ExpectedSHA256: strings.Repeat("0", 64),
			Old:            "return a * b",
			New:            "return a / b",
		},
		{
			Path:           "math.go",
			ExpectedSHA256: sha,
			Old:            "a",
			New:            "x",
		},
		{
			Path:           "math.go",
			ExpectedSHA256: sha,
			Old:            "something missing",
			New:            "x",
		},
	}

	for _, p := range cases {
		if _, err := Apply(
			ctx, w.Path, p,
		); err == nil {
			t.Fatalf(
				"invalid patch accepted: %+v",
				p,
			)
		}
	}
}

func TestDay17VerifyFailIsEvidence(
	t *testing.T,
) {
	src, base := fixture(t, true)

	ctx := context.Background()

	w, err := Prepare(
		ctx, src, base, "failing",
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := VerifyGo(
		ctx,
		w.Path,
		15*time.Second,
	)

	if err != nil {
		t.Fatal(err)
	}

	if result.Passed ||
		result.ExitCode == 0 ||
		!strings.Contains(
			result.Output,
			"FAIL",
		) {
		t.Fatalf(
			"unexpected verification result: %+v",
			result,
		)
	}
}

func TestDay17DirtySourceIsRejected(
	t *testing.T,
) {
	src, base := fixture(t, true)

	if err := os.WriteFile(
		filepath.Join(src, "untracked.txt"),
		[]byte("dirty"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := Prepare(
		context.Background(),
		src, base, "dirty",
	); err == nil {
		t.Fatal("dirty source accepted")
	}
}

func TestDay17UnmanagedRepoRejected(
	t *testing.T,
) {
	src, _ := fixture(t, true)

	if _, err := OpenManaged(
		context.Background(),
		src,
	); err == nil {
		t.Fatal(
			"ordinary source repo accepted as managed worktree",
		)
	}
}
