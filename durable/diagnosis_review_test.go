package durable

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime-app/repodiag"
	"testing"
	"time"
)

type reviewDiagnosticModel struct {
	Passed bool
	Direct bool
	Forged bool
}

func (m reviewDiagnosticModel) Next(ctx context.Context, history []json.RawMessage) (repodiag.Turn, error) {
	if len(history) == 1 && !m.Direct {
		raw := json.RawMessage(`{"type":"function_call","call_id":"observed-tests","name":"run_go_tests","arguments":"{}"}`)
		return repodiag.Turn{Items: []json.RawMessage{raw}, Calls: []repodiag.ToolCall{{CallID: "observed-tests", Name: "run_go_tests", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	status := "failed"
	if m.Passed {
		status = "passed"
	}
	report := &repodiag.DiagnosticReport{Version: repodiag.DiagnosticVersion, TestStatus: status, Conclusion: "fixture diagnosis", Limitations: []string{"Evidence consistency does not prove the root cause."}}
	if !m.Direct {
		var item struct {
			Output string `json:"output"`
		}
		_ = json.Unmarshal(history[len(history)-1], &item)
		var output struct {
			ArtifactSHA256 string `json:"artifact_sha256"`
		}
		_ = json.Unmarshal([]byte(item.Output), &output)
		ref := repodiag.EvidenceRef{CallID: "observed-tests", ArtifactSHA256: output.ArtifactSHA256}
		if m.Forged {
			ref.CallID = "another-run-output"
		}
		report.Evidence = []repodiag.EvidenceRef{ref}
	}
	return repodiag.Turn{Text: "fixture structured report", Diagnosis: report}, nil
}
func TestReviewDiagnosticCompletionIsNotVerifiedSuccess(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model reviewDiagnosticModel
		want  string
	}{
		{"no_test_claim", reviewDiagnosticModel{Passed: true, Direct: true}, repodiag.InsufficientEvidence},
		{"contradict_failed_test", reviewDiagnosticModel{Passed: true}, repodiag.InconsistentEvidence},
		{"observed_failure", reviewDiagnosticModel{}, repodiag.EvidenceConsistent},
		{"foreign_citation", reviewDiagnosticModel{Forged: true}, repodiag.InconsistentEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, workspace := day12TestWorkspace(t)
			if err := os.WriteFile(filepath.Join(root, "math_test.go"), []byte("package math\nimport \"testing\"\nfunc TestBroken(t *testing.T){t.Fatal(\"controlled failure\")}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			store := newMemoryStore()
			runner := Runner{Store: store, Model: tc.model, Workspace: workspace, Provider: "demo"}
			out, err := runner.Run(context.Background(), "review-"+tc.name, root, "diagnose controlled fixture")
			if err != nil || out.Status != Succeeded || out.OutcomeVerdict != tc.want {
				t.Fatal(out, err)
			}
			loaded, err := store.Load(context.Background(), out.RunID)
			if err != nil || loaded.OutcomeVerdict != tc.want {
				t.Fatal("verdict lost", err)
			}
		})
	}
}
func TestReviewStructuredVerdictPersistsWithFencedCheckpoint(t *testing.T) {
	pool, ctx := day13DB(t)
	root, workspace := day13Workspace(t)
	initial := day13Initial(t, ctx, pool, root)
	lease, err := (LeaseManager{Pool: pool}).Claim(ctx, initial.RunID, "diagnostic-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Store: FencedStore{Pool: pool, Lease: lease}, Model: reviewDiagnosticModel{Passed: true}, Workspace: workspace, Provider: "demo"}
	out, err := runner.Run(ctx, initial.RunID, root, initial.Goal)
	if err != nil || out.OutcomeVerdict != repodiag.EvidenceConsistent {
		t.Fatal(out, err)
	}
	got, err := (PostgresStore{Pool: pool}).Load(ctx, initial.RunID)
	if err != nil || got.Diagnosis == nil || got.Diagnosis.TestStatus != "passed" || got.OutcomeVerdict != repodiag.EvidenceConsistent {
		t.Fatal("structured evidence not persisted", got, err)
	}
}
