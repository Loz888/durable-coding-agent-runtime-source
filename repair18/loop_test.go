package repair18

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runtime-app/repair17"
)

func gitCmd(
	t *testing.T,
	src string,
	args ...string,
) {
	t.Helper()

	argv := append(
		[]string{"-C", src},
		args...,
	)

	cmd := exec.Command("git", argv...)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf(
			"git %v: %v: %s",
			args, err, string(out),
		)
	}
}

// 创建独立受控 Git 仓库和 Worktree。
func setup(
	t *testing.T,
	good bool,
) (repair17.Workspace, string) {
	t.Helper()

	dir := t.TempDir()

	src := filepath.Join(dir, "source")

	if err := os.MkdirAll(
		src, 0755,
	); err != nil {
		t.Fatal(err)
	}

	body := "package example\n" +
		"func Divide(a,b int) int { return a * b }\n"

	if good {
		body = "package example\n" +
			"func Divide(a,b int) int { return a / b }\n"
	}

	files := map[string]string{
		"go.mod":  "module example.com/day18fixture\n\ngo 1.23\n",
		"math.go": body,
		"math_test.go": `package example
import "testing"
func TestDivide(t *testing.T) {
    if Divide(12,3)!=4 {
        t.Fatal("wrong divide")
    }
}
`,
	}

	for name, data := range files {
		err := os.WriteFile(
			filepath.Join(src, name),
			[]byte(data),
			0644,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	gitCmd(t, src, "init", "-q")
	gitCmd(t, src, "add", ".")

	gitCmd(
		t, src,
		"-c", "user.name=Day18 Test",
		"-c", "user.email=test@example.invalid",
		"commit", "-qm", "fixture",
	)

	w, err := repair17.Prepare(
		context.Background(),
		src,
		filepath.Join(dir, "worktrees"),
		"run-1",
	)
	if err != nil {
		t.Fatal(err)
	}

	return w, src
}

// Fake Model：按照固定顺序返回建议。
type responseModel struct {
	count   int
	returns []Suggestion
}

func (f *responseModel) Suggest(
	_ context.Context,
	r Request,
) (Suggestion, error) {
	if f.count >= len(f.returns) {
		return Suggestion{},
			errors.New("no more scripted responses")
	}

	v := f.returns[f.count]
	f.count++

	return v, nil
}

func TestDay18OneRoundRepair(t *testing.T) {
	w, src := setup(t, false)

	result, err := Run(
		context.Background(),
		ScriptedFix{},
		Options{
			Worktree:    w.Path,
			File:        "math.go",
			MaxRounds:   2,
			TestTimeout: 10 * time.Second,
		},
	)

	if err != nil ||
		result.Status != "repaired" ||
		result.Baseline.Passed ||
		!result.LastTest.Passed ||
		len(result.Steps) != 1 ||
		result.Diff == "" {
		t.Fatalf(
			"result=%+v err=%v",
			result, err,
		)
	}

	original, err := os.ReadFile(
		filepath.Join(src, "math.go"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(
		string(original),
		"return a * b",
	) {
		t.Fatal("original source changed")
	}
}

func TestDay18TwoRoundFeedback(t *testing.T) {
	w, _ := setup(t, false)

	model := &responseModel{
		returns: []Suggestion{
			{
				Old:          "return a * b",
				New:          "return a + b",
				Reason:       "first wrong change",
				PromptTokens: 12,
			},
			{
				Old:          "return a + b",
				New:          "return a / b",
				Reason:       "second correct change",
				PromptTokens: 13,
			},
		},
	}

	result, err := Run(
		context.Background(),
		model,
		Options{
			Worktree:    w.Path,
			File:        "math.go",
			MaxRounds:   2,
			TestTimeout: 10 * time.Second,
		},
	)

	if err != nil ||
		result.Status != "repaired" ||
		len(result.Steps) != 2 ||
		result.Steps[0].TestPassed ||
		!result.Steps[1].TestPassed ||
		result.PromptTokens != 25 {
		t.Fatalf(
			"result=%+v err=%v",
			result, err,
		)
	}
}

func TestDay18BudgetExhaustion(t *testing.T) {
	w, _ := setup(t, false)

	model := &responseModel{
		returns: []Suggestion{
			{
				Old: "return a * b",
				New: "return a + b",
			},
			{
				Old: "return a + b",
				New: "return a - b",
			},
		},
	}

	result, err := Run(
		context.Background(),
		model,
		Options{
			Worktree:    w.Path,
			File:        "math.go",
			MaxRounds:   2,
			TestTimeout: 10 * time.Second,
		},
	)

	if err != nil ||
		result.Status != "exhausted" ||
		model.count != 2 ||
		len(result.Steps) != 2 ||
		result.LastTest.Passed {
		t.Fatalf(
			"%+v %v",
			result, err,
		)
	}
}

func TestDay18AlreadyPassingDoesNotCallModel(
	t *testing.T,
) {
	w, _ := setup(t, true)

	model := &responseModel{}

	result, err := Run(
		context.Background(),
		model,
		Options{
			Worktree:    w.Path,
			File:        "math.go",
			MaxRounds:   2,
			TestTimeout: 10 * time.Second,
		},
	)

	if err != nil ||
		result.Status != "already_passed" ||
		model.count != 0 {
		t.Fatalf(
			"%+v %v",
			result, err,
		)
	}
}

func TestDay18RejectInvalidPatch(t *testing.T) {
	w, _ := setup(t, false)

	model := &responseModel{
		returns: []Suggestion{
			{
				Old: "not present",
				New: "x",
			},
		},
	}

	result, err := Run(
		context.Background(),
		model,
		Options{
			Worktree:    w.Path,
			File:        "math.go",
			MaxRounds:   2,
			TestTimeout: 10 * time.Second,
		},
	)

	if err != nil ||
		result.Status != "rejected" ||
		model.count != 1 ||
		len(result.Steps) != 1 ||
		result.Steps[0].Error == "" {
		t.Fatalf(
			"%+v %v",
			result, err,
		)
	}
}

func TestDay18RejectTestFile(t *testing.T) {
	w, _ := setup(t, false)

	_, err := Run(
		context.Background(),
		ScriptedFix{},
		Options{
			Worktree:    w.Path,
			File:        "math_test.go",
			MaxRounds:   2,
			TestTimeout: 10 * time.Second,
		},
	)

	if err == nil {
		t.Fatal("test file accepted")
	}
}

func TestDay18RejectInvalidBudget(t *testing.T) {
	_, err := Run(
		context.Background(),
		ScriptedFix{},
		Options{
			MaxRounds:   6,
			TestTimeout: 10 * time.Second,
		},
	)

	if err == nil {
		t.Fatal("unlimited budget accepted")
	}
}

func TestDay18VerifyGoDoesNotExposeAPIKey(
	t *testing.T,
) {
	w, _ := setup(t, true)

	t.Setenv(
		"DEEPSEEK_API_KEY",
		"secret-must-not-leak",
	)

	testSource := `package example

import (
    "os"
    "testing"
)

func TestSecretsNotInherited(t *testing.T) {
    if os.Getenv("DEEPSEEK_API_KEY") != "" {
        t.Fatal("secret leaked")
    }
}
`

	err := os.WriteFile(
		filepath.Join(w.Path, "secret_test.go"),
		[]byte(testSource),
		0644,
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := repair17.VerifyGo(
		context.Background(),
		w.Path,
		15*time.Second,
	)

	if err != nil || !result.Passed {
		t.Fatalf(
			"test=%+v err=%v",
			result, err,
		)
	}
}
