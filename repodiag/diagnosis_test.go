package repodiag

import (
	"encoding/json"
	"testing"
)

func TestDiagnosticEvidenceHashSurvivesInvalidUTF8LogBytes(t *testing.T) {
	code := 0
	result := ToolResult{Name: "run_go_tests", Output: string([]byte{'o', 'k', 0xff, 0xfe}), Executed: true, ExitCode: &code}
	encoded := result.OutputEnvelope()
	var decoded struct {
		ArtifactSHA256 string `json:"artifact_sha256"`
	}
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal(map[string]string{"type": "function_call_output", "call_id": "test-utf8", "output": encoded})
	history := []json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"test-utf8","name":"run_go_tests","arguments":"{}"}`), output}
	report := &DiagnosticReport{Version: DiagnosticVersion, TestStatus: "passed", Evidence: []EvidenceRef{{CallID: "test-utf8", ArtifactSHA256: decoded.ArtifactSHA256}}, Conclusion: "Process exit observed", Limitations: []string{"No semantic root-cause proof."}}
	if got := VerifyDiagnostic(report, history); got != EvidenceConsistent {
		t.Fatal("JSON changed the evidence hash", got)
	}
}
