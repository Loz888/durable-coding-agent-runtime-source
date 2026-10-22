package repair20

import (
	"context"
	"errors"
	"fmt"
	"time"

	"runtime-app/internal/boundedlog"
	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
)

type Ledger interface {
	Ensure(
		context.Context,
		repair19.Ref,
		string,
		string,
		int,
	) (repair17.Workspace, error)

	Recoverable(
		context.Context,
		repair19.Ref,
	) ([]repair19.Intent, error)

	Summary(
		context.Context,
		string,
	) (int, int, error)

	Prepare(
		context.Context,
		repair19.Ref,
		string,
		repair17.Patch,
	) (repair19.Intent, error)

	ApplyOrRecover(
		context.Context,
		repair19.Ref,
		string,
		func(),
	) (repair19.Outcome, error)

	Verify(
		context.Context,
		repair19.Ref,
		string,
		time.Duration,
	) (repair17.TestResult, error)
}

type Agent struct {
	InputPolicy    string
	ResponseCache  bool
	Files          []string
	ExpectedCommit string
	VerifyGo       func(context.Context, string, time.Duration) (repair17.TestResult, error)
	Ledger         Ledger
	Model          repair18.Proposer
	Base           string
	File           string
	MaxRounds      int
	TestTimeout    time.Duration
}

type Result struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	RoundsUsed int    `json:"rounds_used"`
	MaxRounds  int    `json:"max_rounds"`
	LastPatch  string `json:"last_patch,omitempty"`
	TestPassed bool   `json:"test_passed"`
	Diff       string `json:"diff,omitempty"`
}

// All patch writes go through the fenced Ledger.
// The budget belongs to the logical Run, not a Worker.
func (a Agent) Run(
	ctx context.Context,
	ref repair19.Ref,
	source string,
) (Result, error) {
	out := Result{RunID: ref.ID}
	targets, targetErr := a.targets()
	if targetErr != nil {
		return out, targetErr
	}
	if !repair17.ValidInputPolicy(a.InputPolicy) {
		return out, errors.New("unknown sealed input policy")
	}
	a.File = targets[0]
	a.Files = targets

	if a.Ledger == nil ||
		a.Model == nil ||
		a.MaxRounds < 1 ||
		a.MaxRounds > 5 ||
		a.TestTimeout <= 0 ||
		a.TestTimeout > 5*time.Minute ||
		a.File == "" ||
		a.Base == "" {

		return out, errors.New(
			"invalid durable repair configuration",
		)
	}

	w, err := a.Ledger.Ensure(
		ctx,
		ref,
		source,
		a.Base,
		a.MaxRounds,
	)
	if err != nil {
		return out, err
	}

	if a.ExpectedCommit != "" && w.Commit != a.ExpectedCommit {
		return out, errors.New("registered source commit mismatch")
	}
	if _, err = repair17.Inspect(
		ctx,
		w.Path,
		a.File,
	); err != nil {
		return out, err
	}

	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}

		// The first action after recovery is
		// to inspect persistent Patch history.
		ops, err := a.Ledger.Recoverable(
			ctx, ref,
		)
		if err != nil {
			return out, err
		}

		var failure repair17.TestResult

		if len(ops) != 0 {
			last := ops[len(ops)-1]
			out.LastPatch = last.ID

			switch last.State {
			case "prepared":
				_, err = a.Ledger.ApplyOrRecover(
					ctx,
					ref,
					last.ID,
					nil,
				)
				if err != nil {
					return out, err
				}
				fallthrough

			case "applied":
				failure, err = a.Ledger.Verify(
					ctx,
					ref,
					last.ID,
					a.TestTimeout,
				)
				if err != nil {
					return out, err
				}

			case "verified_pass", "verified_fail":
				// Day19 Verify returns cached verdict
				// for an already verified Patch.
				failure, err = a.Ledger.Verify(
					ctx,
					ref,
					last.ID,
					a.TestTimeout,
				)
				if err != nil {
					return out, err
				}

			default:
				return out, fmt.Errorf(
					"unknown patch state: %q",
					last.State,
				)
			}

			if failure.Passed {
				out.Status = "repaired"
				out.TestPassed = true
				break
			}

		} else {
			// Baseline test does not spend
			// one Repair Round.
			verify := a.VerifyGo
			if verify == nil {
				verify = repair17.VerifyGo
				if a.InputPolicy != "" {
					verify = func(c context.Context, path string, t time.Duration) (repair17.TestResult, error) {
						return repair17.VerifyWithPolicy(c, path, a.InputPolicy, t)
					}
				}
			}
			failure, err = verify(
				ctx,
				w.Path,
				a.TestTimeout,
			)
			if err != nil {
				return out, err
			}

			if failure.Passed {
				out.Status = "already_passed"
				out.TestPassed = true
				break
			}
		}

		used, max, err := a.Ledger.Summary(
			ctx, ref.ID,
		)
		if err != nil {
			return out, err
		}

		if used >= max {
			out.Status = "exhausted"
			break
		}

		request, err := a.currentRequest(ctx, w, used+1, failure)
		if err != nil {
			return out, err
		}
		var proposal repair18.Suggestion
		if a.ResponseCache {
			cache, ok := a.Ledger.(interface {
				Propose(context.Context, repair19.Ref, repair18.Request, repair18.Proposer, time.Duration) (repair18.Suggestion, error)
			})
			if !ok {
				return out, errors.New("ledger does not support durable model responses")
			}
			policy := "logical-model-context-v1"
			if a.InputPolicy != "" {
				policy = a.InputPolicy + ":" + policy
			}
			request.ContextDigest, err = repair17.VerificationDigest(ctx, w.Path, policy, a.TestTimeout)
			if err != nil {
				return out, err
			}
			proposal, err = cache.Propose(ctx, ref, request, a.Model, a.TestTimeout)
		} else {
			proposal, err = a.Model.Suggest(ctx, request)
		}
		if err != nil {
			return out, err
		}
		patches, err := proposalPatches(request, proposal)
		if err != nil {
			return out, err
		}

		// Deterministic PatchID for the round.
		patchID := fmt.Sprintf(
			"repair-%03d",
			used+1,
		)

		if len(patches) == 1 {
			_, err = a.Ledger.Prepare(ctx, ref, patchID, patches[0])
		} else if batch, ok := a.Ledger.(interface {
			PrepareBatch(context.Context, repair19.Ref, string, []repair17.Patch) (repair19.Intent, error)
		}); ok {
			_, err = batch.PrepareBatch(ctx, ref, patchID, patches)
		} else {
			return out, errors.New("ledger does not support multi-file intents")
		}
		if err != nil {
			return out, err
		}

		// Loop again via the durable Intent.
		// Recovery takes this same path.
	}

	out.RoundsUsed, out.MaxRounds, err =
		a.Ledger.Summary(ctx, ref.ID)

	if err != nil {
		return out, err
	}

	out.Diff, err = repair17.Diff(
		ctx,
		w.Path,
	)

	return out, err
}

func clip(s string, n int) string { return boundedlog.Clip(s, n) }
