package repodiag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
)

const DiagnosticVersion = "diagnosis-v1"
const (
	EvidenceConsistent   = "evidence_consistent"
	InsufficientEvidence = "insufficient_evidence"
	InconsistentEvidence = "inconsistent_evidence"
)

type EvidenceRef struct {
	CallID         string `json:"call_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}
type DiagnosticReport struct {
	Version     string        `json:"version"`
	TestStatus  string        `json:"test_status"`
	Evidence    []EvidenceRef `json:"evidence"`
	Locations   []string      `json:"locations"`
	Conclusion  string        `json:"conclusion"`
	Limitations []string      `json:"limitations"`
}

func (d *DiagnosticReport) Validate() error {
	if d == nil {
		return nil
	}
	if d.Version != DiagnosticVersion || len(d.Conclusion) == 0 || len(d.Conclusion) > 4000 || len(d.Evidence) > 16 || len(d.Locations) > 16 || len(d.Limitations) == 0 || len(d.Limitations) > 8 {
		return errors.New("invalid bounded diagnostic report")
	}
	if d.TestStatus != "passed" && d.TestStatus != "failed" && d.TestStatus != "not_run" && d.TestStatus != "unknown" {
		return errors.New("invalid observed test status")
	}
	for _, l := range d.Limitations {
		if len(l) == 0 || len(l) > 512 {
			return errors.New("invalid diagnostic limitation")
		}
	}
	for _, p := range d.Locations {
		if len(p) > 256 || !filepath.IsLocal(p) {
			return errors.New("diagnostic location must be repository-relative")
		}
	}
	for _, e := range d.Evidence {
		if len(e.CallID) == 0 || len(e.CallID) > 256 || len(e.ArtifactSHA256) != 64 {
			return errors.New("invalid diagnostic evidence reference")
		}
	}
	return nil
}
func DecodeDiagnostic(text string) (*DiagnosticReport, error) {
	var d DiagnosticReport
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing diagnostic report")
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// OutputEnvelope binds citations to server-generated output, never to model assertions.
func (t ToolResult) OutputEnvelope() string {
	// Hash the same UTF-8 text that JSON actually persists.
	output := strings.ToValidUTF8(t.Output, "\uFFFD")
	digest := sha256.Sum256([]byte(output))
	payload, _ := json.Marshal(struct {
		Output         string `json:"output"`
		IsError        bool   `json:"is_error"`
		Executed       bool   `json:"executed"`
		ExitCode       *int   `json:"exit_code"`
		TimedOut       bool   `json:"timed_out"`
		ArtifactSHA256 string `json:"artifact_sha256"`
	}{output, t.IsError, t.Executed, t.ExitCode, t.TimedOut, hex.EncodeToString(digest[:])})
	return string(payload)
}

// VerifyDiagnostic checks provenance and the latest observed test result.
// It does NOT prove the semantic root cause or arbitrary code correctness.
func VerifyDiagnostic(d *DiagnosticReport, history []json.RawMessage) string {
	if d == nil {
		return InsufficientEvidence
	}
	if d.Validate() != nil {
		return InconsistentEvidence
	}
	calls := map[string]string{}
	outputs := map[string]struct {
		Output         string `json:"output"`
		IsError        bool   `json:"is_error"`
		Executed       bool   `json:"executed"`
		ExitCode       *int   `json:"exit_code"`
		TimedOut       bool   `json:"timed_out"`
		ArtifactSHA256 string `json:"artifact_sha256"`
	}{}
	latest := ""
	for _, raw := range history {
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
			Output string `json:"output"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return InconsistentEvidence
		}
		if item.Type == "function_call" {
			if _, exists := calls[item.CallID]; exists {
				return InconsistentEvidence
			}
			calls[item.CallID] = item.Name
		}
		if item.Type == "function_call_output" {
			if _, ok := calls[item.CallID]; !ok {
				return InconsistentEvidence
			}
			if _, ok := outputs[item.CallID]; ok {
				return InconsistentEvidence
			}
			out := outputs[item.CallID]
			if json.Unmarshal([]byte(item.Output), &out) != nil {
				return InconsistentEvidence
			}
			outputs[item.CallID] = out
			if calls[item.CallID] == "run_go_tests" {
				latest = item.CallID
			}
		}
	}
	cited := map[string]bool{}
	for _, ref := range d.Evidence {
		out, ok := outputs[ref.CallID]
		if !ok || cited[ref.CallID] {
			return InconsistentEvidence
		}
		hash := sha256.Sum256([]byte(out.Output))
		if ref.ArtifactSHA256 != hex.EncodeToString(hash[:]) || out.ArtifactSHA256 != ref.ArtifactSHA256 {
			return InconsistentEvidence
		}
		cited[ref.CallID] = true
	}
	if latest == "" || !cited[latest] {
		return InsufficientEvidence
	}
	out := outputs[latest]
	if !out.Executed || out.ExitCode == nil || *out.ExitCode < 0 || out.TimedOut {
		return InsufficientEvidence
	}
	observed := "failed"
	if *out.ExitCode == 0 && !out.IsError {
		observed = "passed"
	} else if *out.ExitCode == 0 || !out.IsError {
		return InsufficientEvidence
	}
	if d.TestStatus != observed {
		return InconsistentEvidence
	}
	return EvidenceConsistent
}
