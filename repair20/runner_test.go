package repair20

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
)

type fakeLedger struct {
	w       repair17.Workspace
	ops     []repair19.Intent
	patches map[string]repair17.Patch

	used int
	max  int

	applyCount  int
	verifyCount int
	ensureCount int

	crashAfterWrite bool
}

func (f *fakeLedger) Ensure(
	_ context.Context,
	_ repair19.Ref,
	_,
	_ string,
	max int,
) (repair17.Workspace, error) {
	f.ensureCount++

	if f.max == 0 {
		f.max = max
	}

	return f.w, nil
}

func (f *fakeLedger) Recoverable(
	context.Context,
	repair19.Ref,
) ([]repair19.Intent, error) {
	return append(
		[]repair19.Intent(nil),
		f.ops...,
	), nil
}

func (f *fakeLedger) Summary(
	context.Context,
	string,
) (int, int, error) {
	return f.used, f.max, nil
}

func (f *fakeLedger) Prepare(
	_ context.Context,
	_ repair19.Ref,
	id string,
	p repair17.Patch,
) (repair19.Intent, error) {

	if f.used >= f.max {
		return repair19.Intent{}, repair19.ErrBudget
	}

	for _, x := range f.ops {
		if x.ID == id {
			return x, nil
		}
	}

	raw, err := os.ReadFile(
		filepath.Join(f.w.Path, p.Path),
	)
	if err != nil {
		return repair19.Intent{}, err
	}

	if bytes.Count(
		raw, []byte(p.Old),
	) != 1 {
		return repair19.Intent{}, repair19.ErrConflict
	}

	after := repair19.Hash(
		bytes.Replace(
			raw,
			[]byte(p.Old),
			[]byte(p.New),
			1,
		),
	)

	f.used++

	x := repair19.Intent{
		ID:     id,
		File:   p.Path,
		Before: p.ExpectedSHA256,
		After:  after,
		State:  "prepared",
	}

	f.ops = append(f.ops, x)
	f.patches[id] = p

	return x, nil
}

func (f *fakeLedger) ApplyOrRecover(
	ctx context.Context,
	_ repair19.Ref,
	id string,
	_ func(),
) (repair19.Outcome, error) {

	last := &f.ops[len(f.ops)-1]

	if last.ID != id {
		return repair19.Outcome{},
			errors.New("bad patch")
	}

	p := f.patches[id]

	sha, err := repair17.Inspect(
		ctx,
		f.w.Path,
		p.Path,
	)
	if err != nil {
		return repair19.Outcome{}, err
	}

	decision := repair19.Decide(
		sha,
		last.Before,
		last.After,
	)

	if decision == repair19.Conflict {
		return repair19.Outcome{},
			repair19.ErrConflict
	}

	if sha == p.ExpectedSHA256 {
		_, err = repair17.Apply(
			ctx,
			f.w.Path,
			p,
		)

		if err != nil {
			return repair19.Outcome{}, err
		}

		f.applyCount++
		decision = repair19.ApplyNow

	} else {
		decision = repair19.RecoverApplied
	}

	if f.crashAfterWrite {
		f.crashAfterWrite = false

		return repair19.Outcome{},
			errors.New(
				"simulated crash after file write",
			)
	}

	last.State = "applied"

	return repair19.Outcome{
		Intent: *last,
		Action: decision,
	}, nil
}

func (f *fakeLedger) Verify(
	ctx context.Context,
	_ repair19.Ref,
	_ string,
	d time.Duration,
) (repair17.TestResult, error) {

	last := &f.ops[len(f.ops)-1]

	if last.State == "verified_pass" {
		return repair17.TestResult{
			Passed: true,
		}, nil
	}

	if last.State == "verified_fail" {
		return repair17.TestResult{
			Passed: false,
			Output: "previous failure",
		}, nil
	}

	f.verifyCount++

	result, err := repair17.VerifyGo(
		ctx,
		f.w.Path,
		d,
	)
	if err != nil {
		return result, err
	}

	if result.Passed {
		last.State = "verified_pass"
	} else {
		last.State = "verified_fail"
	}

	return result, nil
}

// Each test creates a fresh Git repository
// and an isolated Git Worktree.
func fixture(
	t *testing.T,
	good bool,
) (repair17.Workspace, string, string) {

	t.Helper()

	root := t.TempDir()
	source := filepath.Join(root, "source")

	if err := os.MkdirAll(
		source, 0755,
	); err != nil {
		t.Fatal(err)
	}

	code := "package example\n" +
		"func Divide(a,b int) int { return a * b }\n"

	if good {
		code = "package example\n" +
			"func Divide(a,b int) int { return a / b }\n"
	}

	files := map[string]string{
		"go.mod": "module example.com/day20fixture\n" +
			"\ngo 1.23\n",

		"math.go": code,

		"math_test.go": "package example\n" +
			"import \"testing\"\n" +
			"func TestDivide(t *testing.T){" +
			"if Divide(12,3)!=4 {" +
			"t.Fatal(\"bad divide\")}}\n",
	}

	for name, text := range files {
		err := os.WriteFile(
			filepath.Join(source, name),
			[]byte(text),
			0644,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	commands := [][]string{
		{"init", "-q"},
		{"add", "."},
		{
			"-c", "user.name=Day20",
			"-c", "user.email=day20@example.invalid",
			"commit", "-qm", "baseline",
		},
	}

	for _, args := range commands {
		cmd := exec.Command("git", args...)
		cmd.Dir = source

		output, err := cmd.CombinedOutput()

		if err != nil {
			t.Fatalf(
				"git %v: %v %s",
				args, err, output,
			)
		}
	}

	base := filepath.Join(root, "worktrees")

	w, err := repair17.Prepare(
		context.Background(),
		source,
		base,
		"test-day20",
	)
	if err != nil {
		t.Fatal(err)
	}

	return w, source, base
}

type countModel struct {
	n           int
	suggestions []repair18.Suggestion
}

func (m *countModel) Suggest(
	_ context.Context,
	_ repair18.Request,
) (repair18.Suggestion, error) {

	m.n++

	if m.n > len(m.suggestions) {
		return repair18.Suggestion{},
			errors.New("unexpected model call")
	}

	return m.suggestions[m.n-1], nil
}

func newAgent(
	f *fakeLedger,
	m repair18.Proposer,
	base string,
) Agent {
	return Agent{
		Ledger:      f,
		Model:       m,
		Base:        base,
		File:        "math.go",
		MaxRounds:   2,
		TestTimeout: 10 * time.Second,
	}
}

func TestDay20FreshRepair(t *testing.T) {
	w, src, base := fixture(t, false)

	f := &fakeLedger{
		w:       w,
		patches: map[string]repair17.Patch{},
	}

	m := &countModel{
		suggestions: []repair18.Suggestion{
			{
				Old: "return a * b",
				New: "return a / b",
			},
		},
	}

	got, err := newAgent(f, m, base).Run(
		context.Background(),
		repair19.Ref{
			ID:    w.RunID,
			Owner: "A",
			Epoch: 1,
		},
		src,
	)

	if err != nil ||
		got.Status != "repaired" ||
		got.RoundsUsed != 1 ||
		m.n != 1 ||
		f.applyCount != 1 {

		t.Fatalf(
			"%+v %v model=%d applies=%d",
			got, err, m.n, f.applyCount,
		)
	}

	srcBytes, err := os.ReadFile(
		filepath.Join(src, "math.go"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(
		string(srcBytes), "return a * b",
	) {
		t.Fatal("original source polluted")
	}
}

func TestDay20ResumeAppliedWithoutModelCall(
	t *testing.T,
) {
	w, src, base := fixture(t, false)

	f := &fakeLedger{
		w:               w,
		patches:         map[string]repair17.Patch{},
		crashAfterWrite: true,
	}

	m := &countModel{
		suggestions: []repair18.Suggestion{
			{
				Old: "return a * b",
				New: "return a / b",
			},
		},
	}

	a := newAgent(f, m, base)

	ref := repair19.Ref{
		ID:    w.RunID,
		Owner: "A",
		Epoch: 1,
	}

	if _, err := a.Run(
		context.Background(),
		ref,
		src,
	); err == nil {
		t.Fatal("expected crash")
	}

	got, err := a.Run(
		context.Background(),
		repair19.Ref{
			ID:    w.RunID,
			Owner: "B",
			Epoch: 2,
		},
		src,
	)

	if err != nil ||
		got.Status != "repaired" ||
		m.n != 1 ||
		f.used != 1 ||
		f.applyCount != 1 {

		t.Fatalf(
			"%+v %v model=%d",
			got, err, m.n,
		)
	}
}

func TestDay20TwoRoundBudgetShared(t *testing.T) {
	w, src, base := fixture(t, false)

	f := &fakeLedger{
		w:       w,
		patches: map[string]repair17.Patch{},
	}

	m := &countModel{
		suggestions: []repair18.Suggestion{
			{
				Old: "return a * b",
				New: "return a + b",
			},
			{
				Old: "return a + b",
				New: "return a / b",
			},
		},
	}

	got, err := newAgent(f, m, base).Run(
		context.Background(),
		repair19.Ref{
			ID:    w.RunID,
			Owner: "A",
			Epoch: 1,
		},
		src,
	)

	if err != nil ||
		got.Status != "repaired" ||
		got.RoundsUsed != 2 ||
		m.n != 2 {

		t.Fatalf("%+v %v", got, err)
	}
}

func TestDay20ExhaustedAndCached(
	t *testing.T,
) {
	w, src, base := fixture(t, false)

	f := &fakeLedger{
		w:       w,
		patches: map[string]repair17.Patch{},
	}

	m := &countModel{
		suggestions: []repair18.Suggestion{
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

	a := newAgent(f, m, base)

	ref := repair19.Ref{
		ID:    w.RunID,
		Owner: "A",
		Epoch: 1,
	}

	got, err := a.Run(
		context.Background(),
		ref,
		src,
	)

	if err != nil ||
		got.Status != "exhausted" ||
		m.n != 2 {
		t.Fatalf("%+v %v", got, err)
	}

	// Starting the runner again must NOT
	// allocate a third repair round.
	_, err = a.Run(
		context.Background(),
		ref,
		src,
	)

	if err != nil ||
		m.n != 2 ||
		f.used != 2 {

		t.Fatalf(
			"second invocation: %v model=%d",
			err, m.n,
		)
	}
}

func TestDay20AlreadyPassed(t *testing.T) {
	w, src, base := fixture(t, true)

	f := &fakeLedger{
		w:       w,
		patches: map[string]repair17.Patch{},
	}

	m := &countModel{}

	got, err := newAgent(f, m, base).Run(
		context.Background(),
		repair19.Ref{
			ID:    w.RunID,
			Owner: "A",
			Epoch: 1,
		},
		src,
	)

	if err != nil ||
		got.Status != "already_passed" ||
		m.n != 0 {

		t.Fatalf("%+v %v", got, err)
	}
}
