package eval21

import (
	"strings"
	"testing"
)

func TestDay21ScoreDenominators(t *testing.T) {
	m := Manifest{Cases: []Case{
		{ID: "bug", Kind: "repair", Trials: 2},
		{ID: "clean", Kind: "already_passed", Trials: 1},
		{ID: "recovery", Kind: "recovery", Trials: 1},
		{
			ID:        "budget",
			Kind:      "budget_exhausted",
			Trials:    1,
			MaxRounds: 2,
		},
	}}

	runs := []Observation{
		{
			CaseID: "bug", Trial: 1, RunID: "r1",
			Status: "repaired", BaselineFailed: true,
			TestsPassed: true, OriginalClean: true,
			CheckpointSucceeded: true,
			RoundsUsed:          1, Attempts: 1,
			ModelCalls: 1, DurationMS: 10,
		},
		{
			CaseID: "bug", Trial: 2, RunID: "r2",
			Status: "repaired", BaselineFailed: true,
			TestsPassed: false, OriginalClean: true,
			CheckpointSucceeded: true,
			RoundsUsed:          1, Attempts: 1,
			ModelCalls: 1, DurationMS: 20,
		},
		{
			CaseID: "clean", Trial: 1, RunID: "r3",
			Status:      "already_passed",
			TestsPassed: true, OriginalClean: true,
			CheckpointSucceeded: true,
			Attempts:            1, DurationMS: 30,
		},
		{
			CaseID: "recovery", Trial: 1, RunID: "r4",
			Status: "repaired", BaselineFailed: true,
			TestsPassed: true, OriginalClean: true,
			CheckpointSucceeded: true, Recovered: true,
			Attempts: 2, DurationMS: 40,
		},
		{
			CaseID: "budget", Trial: 1, RunID: "r5",
			Status: "exhausted", BaselineFailed: true,
			OriginalClean: true, RoundsUsed: 2,
			Attempts: 1, ModelCalls: 2, DurationMS: 50,
		},
	}

	r, err := Score(m, runs)
	if err != nil {
		t.Fatal(err)
	}

	if r.TotalRuns != 5 ||
		r.ScenarioPassed != 4 ||
		r.RepairPassed != 1 ||
		r.RepairTotal != 2 ||
		r.RecoveryPassed != 1 ||
		r.RecoveryTotal != 1 ||
		r.FalseClaims != 1 ||
		r.PositiveClaims != 4 ||
		r.P50LatencyMS != 30 ||
		r.P95LatencyMS != 50 {
		t.Fatalf("bad denominators: %+v", r)
	}

	if !strings.Contains(
		r.Markdown(),
		"Fresh repair success: **1/2**",
	) {
		t.Fatal(r.Markdown())
	}
}

func TestDay21RejectMissingOrDuplicateTrials(
	t *testing.T,
) {
	m := Manifest{
		Cases: []Case{
			{ID: "a", Kind: "repair", Trials: 2},
		},
	}

	x := Observation{
		CaseID: "a", Trial: 1, RunID: "r1",
		Status: "repaired", Attempts: 1,
		DurationMS: 1,
	}

	if _, err := Score(
		m, []Observation{x},
	); err == nil {
		t.Fatal("missing trial accepted")
	}

	if _, err := Score(
		m, []Observation{x, x},
	); err == nil {
		t.Fatal("duplicate trial accepted")
	}
}

func TestDay21RejectFakeSuccessAndUnknownCase(
	t *testing.T,
) {
	m := Manifest{
		Cases: []Case{
			{ID: "a", Kind: "repair", Trials: 1},
		},
	}

	x := Observation{
		CaseID: "a", Trial: 1, RunID: "r1",
		Status: "repaired", BaselineFailed: true,
		TestsPassed: false, OriginalClean: true,
		CheckpointSucceeded: true, Attempts: 1,
		DurationMS: 1,
	}

	r, err := Score(m, []Observation{x})
	if err != nil {
		t.Fatal(err)
	}

	if r.RepairPassed != 0 ||
		r.FalseClaims != 1 {
		t.Fatal(
			"invalid success not detected",
			r,
		)
	}

	x.CaseID = "unknown"

	if _, err := Score(
		m, []Observation{x},
	); err == nil {
		t.Fatal("unknown case accepted")
	}
}

func TestDay21MockTokensRemainUnknown(
	t *testing.T,
) {
	m := Manifest{
		Cases: []Case{
			{
				ID:     "a",
				Kind:   "already_passed",
				Trials: 1,
			},
		},
	}

	x := Observation{
		CaseID: "a", Trial: 1, RunID: "r",
		Status:              "already_passed",
		TestsPassed:         true,
		OriginalClean:       true,
		CheckpointSucceeded: true,
		Attempts:            1,
	}

	r, err := Score(m, []Observation{x})
	if err != nil {
		t.Fatal(err)
	}

	if r.TokenKnownRuns != 0 {
		t.Fatal(
			"mock token use should be unknown",
		)
	}
}
