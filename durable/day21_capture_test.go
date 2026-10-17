package durable

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runtime-app/eval21"
	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
	"runtime-app/repodiag"
)

// Deterministic proposals, no real API calls.
type day21Proposer struct {
	kind  string
	calls int
}

func (m *day21Proposer) Suggest(
	_ context.Context,
	req repair18.Request,
) (repair18.Suggestion, error) {
	m.calls++

	switch m.kind {
	case "repair":
		return repair18.Suggestion{
			Old: "return a * b",
			New: "return a / b",
		}, nil

	case "unfixable":
		if req.Round == 1 {
			return repair18.Suggestion{
				Old: "return a * b",
				New: "return a + b",
			}, nil
		}

		return repair18.Suggestion{
			Old: "return a + b",
			New: "return a - b",
		}, nil

	default:
		return repair18.Suggestion{},
			fmt.Errorf(
				"unexpected model call for %s",
				m.kind,
			)
	}
}

func day21Git(
	t *testing.T,
	source string,
	args ...string,
) string {
	t.Helper()

	cmd := exec.Command(
		"git",
		append(
			[]string{"-C", source},
			args...,
		)...,
	)

	out, err := cmd.CombinedOutput()

	if err != nil {
		t.Fatalf(
			"git %v: %v %s",
			args, err, out,
		)
	}

	return string(out)
}

func day21Baseline(
	t *testing.T,
	source string,
) bool {
	t.Helper()

	cmd := exec.Command(
		"go",
		"test",
		"-count=1",
		"./...",
	)

	cmd.Dir = source

	cmd.Env = append(
		os.Environ(),
		"GOPROXY=off",
		"GOSUMDB=off",
	)

	_, err := cmd.CombinedOutput()

	return err != nil
}

func day21Save(
	t *testing.T,
	o eval21.Observation,
) {
	t.Helper()

	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf(
		"DAY21_OBSERVATION %s",
		b,
	)

	path := os.Getenv("DAY21_RUNS_OUT")

	if path == "" {
		return
	}

	f, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0600,
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.Write(
		append(b, '\n'),
	); err != nil {
		f.Close()
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func day21FinalStatus(
	s Snapshot,
) string {
	const prefix = "coding repair "

	if !strings.HasPrefix(
		s.Final, prefix,
	) {
		return "unknown"
	}

	return strings.TrimSpace(
		strings.SplitN(
			strings.TrimPrefix(
				s.Final, prefix,
			),
			";",
			2,
		)[0],
	)
}

func day21Case(
	t *testing.T,
	caseID,
	kind string,
	trial int,
) {
	t.Helper()

	pool, ctx := day13DB(t)

	source, base := day20Fixture(t)

	// Construct a genuinely passing baseline.
	if kind == "already_passed" {
		path := filepath.Join(
			source,
			"math.go",
		)

		err := os.WriteFile(
			path,
			[]byte(
				"package example\n"+
					"func Divide(a,b int) int { return a / b }\n",
			),
			0644,
		)
		if err != nil {
			t.Fatal(err)
		}

		day21Git(
			t, source,
			"add", "math.go",
		)

		day21Git(
			t, source,
			"-c", "user.name=Day21",
			"-c", "user.email=day21@example.invalid",
			"commit",
			"--amend",
			"--no-edit",
			"-q",
		)
	}

	baselineFailed := day21Baseline(
		t, source,
	)

	run := day14Run(
		t, pool, source, 2,
	)

	day20SetProvider(
		t,
		ctx,
		pool,
		run.RunID,
		"coding:mock",
	)

	schema := day20Schema(
		t, ctx, pool,
	)

	store, err := repair19.NewStore(
		pool,
		"day12",
		schema,
	)
	if err != nil {
		t.Fatal(err)
	}

	queue := Queue{Pool: pool}

	model := &day21Proposer{
		kind: kind,
	}

	handler := CodingHandler{
		Pool:        pool,
		PatchStore:  store,
		PatchSchema: schema,

		WorktreeBase: base,
		File:         "math.go",
		MaxRounds:    2,

		TestTimeout: 10 * time.Second,
		Proposer:    model,
	}

	var job ClaimedRun
	var started time.Time

	recovered := false

	if kind == "recovery" {

		// Worker A creates a persisted,
		// already-verified successful Patch.
		ownerA := day20Claim(
			t,
			ctx,
			queue,
			run.RunID,
			"day21-A",
		)

		refA := repair19.Ref{
			ID:    run.RunID,
			Owner: ownerA.Lease.Owner,
			Epoch: ownerA.Lease.Epoch,
		}

		w, err := repair17.Prepare(
			ctx,
			source,
			base,
			run.RunID,
		)
		if err != nil {
			t.Fatal(err)
		}

		if err := store.Register(
			ctx,
			refA,
			w.Path,
			2,
		); err != nil {
			t.Fatal(err)
		}

		sha, err := repair17.Inspect(
			ctx,
			w.Path,
			"math.go",
		)
		if err != nil {
			t.Fatal(err)
		}

		_, err = store.Prepare(
			ctx,
			refA,
			"repair-001",
			repair17.Patch{
				Path:           "math.go",
				ExpectedSHA256: sha,
				Old:            "return a * b",
				New:            "return a / b",
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		_, err = store.ApplyOrRecover(
			ctx,
			refA,
			"repair-001",
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}

		verdict, err := store.Verify(
			ctx,
			refA,
			"repair-001",
			10*time.Second,
		)

		if err != nil || !verdict.Passed {
			t.Fatalf(
				"prepare verified_pass: %+v %v",
				verdict, err,
			)
		}

		// Expire only this test Run.
		_, err = pool.Exec(
			ctx,
			`UPDATE day12.agent_runs
             SET
                lease_until =
                    clock_timestamp() - interval '1 second',
                available_at =
                    '-infinity'::timestamptz
             WHERE run_id = $1`,
			run.RunID,
		)
		if err != nil {
			t.Fatal(err)
		}

		// Measure the new Worker's recovery.
		started = time.Now()

		job = day20Claim(
			t,
			ctx,
			queue,
			run.RunID,
			"day21-B",
		)

		recovered =
			job.Lease.Epoch == refA.Epoch+1

	} else {
		started = time.Now()

		job = day20Claim(
			t,
			ctx,
			queue,
			run.RunID,
			"day21-A",
		)
	}

	// Run the EXISTING Scheduler.
	sched := Scheduler{
		Queue: queue,
		Owner: job.Lease.Owner,

		TTL:        60 * time.Second,
		RenewEvery: time.Second,
		RunTimeout: 40 * time.Second,

		Models: func(
			string,
		) (repodiag.Model, error) {
			return nil, fmt.Errorf(
				"coding run must not use diagnosis model factory",
			)
		},

		CodingRun: handler.Execute,
	}

	sched.execute(
		ctx,
		job,
	)

	// Stop the E2E timer immediately
	// after Scheduler execution.
	latency := time.Since(
		started,
	).Milliseconds()

	// Independent evidence collection.
	snap, err := (PostgresStore{
		Pool: pool,
	}).Load(
		ctx,
		run.RunID,
	)
	if err != nil {
		t.Fatal(err)
	}

	used, _, err := store.Summary(
		ctx,
		run.RunID,
	)
	if err != nil {
		t.Fatal(err)
	}

	wpath := filepath.Join(
		base,
		run.RunID,
	)

	proof, err := repair17.VerifyGo(
		ctx,
		wpath,
		10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	clean := strings.TrimSpace(
		day21Git(
			t,
			source,
			"status",
			"--porcelain",
			"--untracked-files=all",
		),
	) == ""

	o := eval21.Observation{
		CaseID: caseID,
		Trial:  trial,
		RunID:  run.RunID,

		Status: day21FinalStatus(snap),

		BaselineFailed: baselineFailed,
		TestsPassed:    proof.Passed,
		OriginalClean:  clean,

		CheckpointSucceeded: snap.Status == Succeeded,

		Recovered:  recovered,
		RoundsUsed: used,

		Attempts:   int(job.Lease.Epoch),
		ModelCalls: model.calls,

		UsageKnown: false,
		DurationMS: latency,
	}

	day21Save(t, o)
} // 12 real executions:
// 4 controlled scenarios * 3 trials.
//
// No DeepSeek API call is performed.
func TestDay21Capture(t *testing.T) {
	cases := []struct {
		id   string
		kind string
	}{
		{
			"divide-repair",
			"repair",
		},
		{
			"baseline-pass",
			"already_passed",
		},
		{
			"two-round-exhaust",
			"unfixable",
		},
		{
			"verified-pass-takeover",
			"recovery",
		},
	}

	for _, c := range cases {
		for trial := 1; trial <= 3; trial++ {
			t.Run(
				fmt.Sprintf(
					"%s/%d",
					c.id,
					trial,
				),
				func(t *testing.T) {
					day21Case(
						t,
						c.id,
						c.kind,
						trial,
					)
				},
			)
		}
	}
}
