package eval16

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func ptr(b bool) *bool {
	return &b
}

func TestDay16ExtractFinal(t *testing.T) {
	raw := "logging\n========== AGENT RESULT ==========\n" +
		`{"goal":"diagnose","final":"file math.go: divide is wrong"}`

	got, err := ExtractFinal(raw)

	if err != nil ||
		got != "file math.go: divide is wrong" {
		t.Fatalf(
			"got=%q err=%v",
			got, err,
		)
	}

	if _, err := ExtractFinal(
		"Agent demo result",
	); err == nil {
		t.Fatal("accepted malformed result")
	}
}

func TestDay16Percentile(t *testing.T) {
	got := Percentile(
		[]int64{40, 10, 30, 20},
		.5,
	)

	if got != 25 {
		t.Fatalf("p50=%v", got)
	}

	if Percentile(nil, .95) != 0 {
		t.Fatal("empty percentile")
	}
}

func TestDay16ManualJudgmentsAndFalsePositive(
	t *testing.T,
) {
	cs := []Case{
		{ID: "bug", HasBug: true},
		{ID: "clean", HasBug: false},
	}

	runs := []Capture{
		{
			CaseID:     "bug",
			RunID:      "r1",
			Report:     "test evidence",
			DurationMS: 100,
		},
		{
			CaseID:     "clean",
			RunID:      "r2",
			Report:     "claims bug",
			DurationMS: 200,
		},
	}

	judges := []Judgment{
		{
			CaseID:           "bug",
			RunID:            "r1",
			ResultCorrect:    ptr(true),
			EvidenceGrounded: ptr(true),
			ReportedBug:      ptr(true),
		},
		{
			CaseID:        "clean",
			RunID:         "r2",
			ResultCorrect: ptr(false),
			ReportedBug:   ptr(true),
		},
	}

	s, err := Evaluate(cs, runs, judges)

	if err != nil ||
		s.Graded != 2 ||
		s.Correct != 1 ||
		s.FalsePositives != 1 ||
		s.EvidenceCorrect != 1 {
		t.Fatalf(
			"summary=%+v err=%v",
			s, err,
		)
	}
}

func TestDay16UngradedIsNotCorrect(t *testing.T) {
	cs := []Case{
		{ID: "bug", HasBug: true},
	}

	runs := []Capture{
		{
			CaseID: "bug",
			RunID:  "r",
			Report: "something",
		},
	}

	judges := []Judgment{
		{
			CaseID: "bug",
			RunID:  "r",
		},
	}

	s, err := Evaluate(cs, runs, judges)

	if err != nil ||
		s.Graded != 0 ||
		s.Correct != 0 {
		t.Fatalf("%+v %v", s, err)
	}

	judges[0].RunID = "old-run"

	if _, err := Evaluate(
		cs, runs, judges,
	); err == nil {
		t.Fatal("stale judgment accepted")
	}
}

func TestDay16RejectDuplicateCases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(
		dir,
		"cases.json",
	)

	data := `[{"id":"same","repo":"a","category":"bug","expected_root_cause":"x","expected_evidence":"y"},{"id":"same","repo":"b","category":"bug","expected_root_cause":"x","expected_evidence":"y"}]`

	if err := os.WriteFile(
		path,
		[]byte(data),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadCases(path); err == nil {
		t.Fatal("duplicates accepted")
	}
}

func TestDay16JSONLRoundTrip(t *testing.T) {
	path := filepath.Join(
		t.TempDir(),
		"runs.jsonl",
	)

	runs := []Capture{
		{
			CaseID: "a",
			RunID:  "1",
			Report: "诊断报告",
		},
	}

	if err := WriteJSONL(
		path, runs,
	); err != nil {
		t.Fatal(err)
	}

	got, err := ReadJSONL[Capture](path)

	if err != nil ||
		len(got) != 1 ||
		!strings.Contains(
			got[0].Report,
			"诊断",
		) {
		t.Fatalf(
			"%+v %v",
			got, err,
		)
	}
}

func TestDay16CaptureWithFakeAdapter(
	t *testing.T,
) {
	// Linux-only integration smoke.
	// This is NOT a real DeepSeek evaluation.
	if runtime.GOOS == "windows" {
		t.Skip("shell smoke runs on Linux")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")

	if err := os.Mkdir(
		repo, 0755,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(repo, "go.mod"),
		[]byte(
			"module example.com/fixture\n\ngo 1.23\n",
		),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(
			repo,
			"fixture_test.go",
		),
		[]byte(`package example
import "testing"
func TestOK(t *testing.T) {}
`),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(
		root,
		"agent.sh",
	)

	script := "#!/bin/sh\n" +
		"printf '%s\\n' " +
		"'========== AGENT RESULT ==========' " +
		"'{\"final\":\"MOCK ONLY\"}'\n"

	if err := os.WriteFile(
		binary,
		[]byte(script),
		0755,
	); err != nil {
		t.Fatal(err)
	}

	opts := CaptureOptions{
		Binary:        binary,
		Mode:          "demo",
		Goal:          "test",
		OracleTimeout: 10 * time.Second,
		AgentTimeout:  10 * time.Second,
	}

	c := Case{
		ID:               "fixture",
		Repo:             repo,
		ExpectGoTestPass: true,
	}

	captured := CaptureCase(
		context.Background(),
		c,
		opts,
	)

	if captured.Error != "" ||
		captured.Report != "MOCK ONLY" ||
		!captured.OraclePass ||
		captured.RunID == "" {
		t.Fatalf(
			"capture=%+v",
			captured,
		)
	}

	// Incorrect ground truth must block evaluation.
	c.ExpectGoTestPass = false

	bad := CaptureCase(
		context.Background(),
		c,
		opts,
	)

	if bad.Error == "" ||
		bad.Report != "" {
		t.Fatalf(
			"oracle mismatch should block agent: %+v",
			bad,
		)
	}
}
