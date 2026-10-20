package durable

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
	"runtime-app/repair20"
)

// CodingHandler plugs into the EXISTING Scheduler.
type CodingHandler struct {
	WrapProposer   func(context.Context, ClaimedRun, repair18.Proposer) (repair18.Proposer, error)
	ExpectedCommit func(context.Context, ClaimedRun) (string, error)
	VerifyGo       func(context.Context, string, time.Duration) (repair17.TestResult, error)
	Pool           *pgxpool.Pool
	PatchStore     *repair19.Store
	PatchSchema    string

	WorktreeBase string
	File         string
	MaxRounds    int
	TestTimeout  time.Duration

	// Test-only dependency injection.
	Proposer repair18.Proposer

	// Test-only crash injection.
	BeforeCheckpoint func()
}

func (h CodingHandler) Execute(
	ctx context.Context,
	job ClaimedRun,
) (Snapshot, error) {

	var zero Snapshot

	if h.Pool == nil ||
		h.PatchStore == nil {
		return zero, errors.New(
			"missing database stores",
		)
	}

	expectedCommit := ""
	if h.ExpectedCommit != nil {
		var e error
		expectedCommit, e = h.ExpectedCommit(ctx, job)
		if e != nil {
			return zero, e
		}
	}
	var model repair18.Proposer

	switch job.Provider {
	case "coding:mock":
		model = repair18.ScriptedFix{}

	case "coding:deepseek":
		model = repair18.DeepSeek{
			APIKey: os.Getenv(
				"DEEPSEEK_API_KEY",
			),
		}

	default:
		return zero, fmt.Errorf(
			"not a coding provider: %s",
			job.Provider,
		)
	}

	if h.Proposer != nil {
		model = h.Proposer
	}

	if h.WrapProposer != nil {
		var e error
		model, e = h.WrapProposer(ctx, job, model)
		if e != nil {
			return zero, e
		}
	}

	agent := repair20.Agent{
		ExpectedCommit: expectedCommit,
		VerifyGo:       h.VerifyGo,
		Ledger: repair20.PostgresLedger{
			Store:  h.PatchStore,
			Schema: h.PatchSchema,
		},

		Model:       model,
		Base:        h.WorktreeBase,
		File:        h.File,
		MaxRounds:   h.MaxRounds,
		TestTimeout: h.TestTimeout,
	}

	outcome, err := agent.Run(
		ctx,
		repair19.Ref{
			ID:    job.RunID,
			Owner: job.Lease.Owner,
			Epoch: job.Lease.Epoch,
		},
		job.Repo,
	)
	if err != nil {
		return zero, err
	}

	// Used exclusively by crash tests.
	if h.BeforeCheckpoint != nil {
		h.BeforeCheckpoint()
	}

	// Use the EXISTING fenced checkpoint store.
	fenced := FencedStore{
		Pool:  h.Pool,
		Lease: job.Lease,
	}

	snapshot, err := fenced.Load(
		ctx,
		job.RunID,
	)
	if err != nil {
		return zero, err
	}

	if snapshot.Status != "running" {
		return zero, fmt.Errorf(
			"unexpected checkpoint status %q",
			snapshot.Status,
		)
	}

	expected := snapshot.Revision

	snapshot.Revision = expected + 1

	if outcome.Status == "exhausted" {
		snapshot.Status = "failed"
	} else {
		snapshot.Status = "succeeded"
	}

	snapshot.Final = fmt.Sprintf(
		"coding repair %s; rounds=%d/%d; "+
			"last_patch=%s; tests_passed=%t",

		outcome.Status,
		outcome.RoundsUsed,
		outcome.MaxRounds,
		outcome.LastPatch,
		outcome.TestPassed,
	)

	// Revision CAS + Lease Fencing.
	if err = fenced.Save(
		ctx,
		snapshot,
		expected,
	); err != nil {
		return zero, err
	}

	return snapshot, nil
}

func IsCodingProvider(provider string) bool {
	return strings.HasPrefix(
		provider,
		"coding:",
	)
}
