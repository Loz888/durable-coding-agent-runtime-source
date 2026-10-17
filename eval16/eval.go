package eval16

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Case struct {
	ID               string `json:"id"`
	Repo             string `json:"repo"`
	Category         string `json:"category"`
	HasBug           bool   `json:"has_bug"`
	ExpectGoTestPass bool   `json:"expect_go_test_pass"`
	ExpectedRoot     string `json:"expected_root_cause"`
	ExpectedEvidence string `json:"expected_evidence"`
}

type Capture struct {
	CaseID       string `json:"case_id"`
	RunID        string `json:"run_id"`
	AgentMode    string `json:"agent_mode"`
	OraclePass   bool   `json:"oracle_pass"`
	OracleOutput string `json:"oracle_output"`
	Report       string `json:"report"`
	DurationMS   int64  `json:"duration_ms"`
	ExitCode     int    `json:"exit_code"`
	Error        string `json:"error,omitempty"`
}

type Judgment struct {
	CaseID           string `json:"case_id"`
	RunID            string `json:"run_id"`
	ResultCorrect    *bool  `json:"result_correct"`
	EvidenceGrounded *bool  `json:"evidence_grounded"`
	ReportedBug      *bool  `json:"reported_bug"`
	Notes            string `json:"notes"`
}

type Summary struct {
	Total           int
	Captured        int
	Graded          int
	Correct         int
	BugGraded       int
	EvidenceCorrect int
	CleanGraded     int
	FalsePositives  int
	P50MS           float64
	P95MS           float64
}

func LoadCases(path string) ([]Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cs []Case
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, err
	}

	if len(cs) == 0 {
		return nil, errors.New("empty manifest")
	}

	seen := map[string]bool{}

	for _, c := range cs {
		if c.ID == "" ||
			c.Repo == "" ||
			c.Category == "" ||
			seen[c.ID] {
			return nil, fmt.Errorf(
				"invalid or duplicate case ID: %q",
				c.ID,
			)
		}

		if c.ExpectedRoot == "" ||
			c.ExpectedEvidence == "" {
			return nil, fmt.Errorf(
				"missing human reference for %s",
				c.ID,
			)
		}

		seen[c.ID] = true
	}

	return cs, nil
}

// ReadJSONL rejects malformed lines rather than
// silently dropping evaluation cases.
func ReadJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(
		make([]byte, 64*1024),
		4*1024*1024,
	)

	var out []T

	for line := 1; scanner.Scan(); line++ {
		b := bytes.TrimSpace(scanner.Bytes())
		if len(b) == 0 {
			continue
		}

		var x T
		if err := json.Unmarshal(b, &x); err != nil {
			return nil, fmt.Errorf(
				"%s:%d: %w",
				path, line, err,
			)
		}
		out = append(out, x)
	}

	return out, scanner.Err()
}

func WriteJSONL[T any](
	path string,
	rows []T,
) error {
	if err := os.MkdirAll(
		filepath.Dir(path), 0755,
	); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}

	return f.Sync()
}

// The existing Day11 CLI previously emitted a
// JSON envelope containing a "final" field.
func ExtractFinal(stdout string) (string, error) {
	const marker = "========== AGENT RESULT =========="

	if at := strings.LastIndex(
		stdout, marker,
	); at >= 0 {
		stdout = stdout[at+len(marker):]
	}

	stdout = strings.TrimSpace(stdout)

	if !strings.HasPrefix(stdout, "{") {
		return "", errors.New(
			"agent output has no JSON result; check adapter",
		)
	}

	var result struct {
		Final string `json:"final"`
	}

	err := json.NewDecoder(
		strings.NewReader(stdout),
	).Decode(&result)

	if err != nil {
		return "", fmt.Errorf(
			"decode Agent JSON: %w", err,
		)
	}

	if strings.TrimSpace(result.Final) == "" {
		return "", errors.New(
			"agent JSON has empty final",
		)
	}

	return result.Final, nil
}

func trimmed(s string, n int) string {
	r := []rune(s)

	if len(r) > n {
		return string(r[:n]) + "...[truncated]"
	}

	return s
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}

	var e *exec.ExitError
	if errors.As(err, &e) {
		return e.ExitCode()
	}

	return -1
}

type CaptureOptions struct {
	Binary        string
	Mode          string
	Goal          string
	OracleTimeout time.Duration
	AgentTimeout  time.Duration
}

// CaptureCase runs actual Go tests and an Agent CLI.
// Only use this on trusted local repositories.
func CaptureCase(
	ctx context.Context,
	c Case,
	opt CaptureOptions,
) Capture {
	out := Capture{
		CaseID: c.ID,
		RunID: fmt.Sprintf(
			"%s-%d",
			c.ID,
			time.Now().UnixNano(),
		),
		AgentMode: opt.Mode,
		ExitCode:  -1,
	}

	repo, err := filepath.Abs(c.Repo)
	if err != nil {
		out.Error = err.Error()
		return out
	}

	if _, err := os.Stat(
		filepath.Join(repo, "go.mod"),
	); err != nil {
		out.Error = "missing go.mod: " + err.Error()
		return out
	}

	// First establish the actual test oracle.
	oc, cancel := context.WithTimeout(
		ctx,
		opt.OracleTimeout,
	)

	oracle := exec.CommandContext(
		oc,
		"go",
		"test",
		"-count=1",
		"./...",
	)
	oracle.Dir = repo

	oracleOutput, oracleErr := oracle.CombinedOutput()

	timedOut := errors.Is(
		oc.Err(),
		context.DeadlineExceeded,
	)
	cancel()

	out.OraclePass = oracleErr == nil
	out.OracleOutput = trimmed(
		string(oracleOutput),
		3000,
	)

	if timedOut ||
		out.OraclePass != c.ExpectGoTestPass {
		out.Error = fmt.Sprintf(
			"invalid fixture: expected go test pass=%t, got=%t (timeout=%t)",
			c.ExpectGoTestPass,
			out.OraclePass,
			timedOut,
		)
		return out
	}

	// Now run the existing Agent as a subprocess.
	ac, stop := context.WithTimeout(
		ctx,
		opt.AgentTimeout,
	)
	defer stop()

	cmd := exec.CommandContext(
		ac,
		opt.Binary,
		"-repo", repo,
		"-mode", opt.Mode,
		"-goal", opt.Goal,
	)

	started := time.Now()

	agentOutput, runErr := cmd.CombinedOutput()

	out.DurationMS = time.Since(
		started,
	).Milliseconds()

	out.ExitCode = exitCode(runErr)

	if ac.Err() != nil {
		out.Error = "agent context: " +
			ac.Err().Error()
		return out
	}

	final, parseErr := ExtractFinal(
		string(agentOutput),
	)

	if parseErr != nil {
		out.Error = parseErr.Error() +
			"; raw Agent logs deliberately omitted"
		return out
	}

	out.Report = final

	if runErr != nil {
		out.Error = "agent exited: " +
			runErr.Error()
	}

	return out
}

func Percentile(
	values []int64,
	fraction float64,
) float64 {
	if len(values) == 0 {
		return 0
	}

	a := append([]int64(nil), values...)

	sort.Slice(
		a,
		func(i, j int) bool {
			return a[i] < a[j]
		},
	)

	index := float64(len(a)-1) * fraction

	lo := int(math.Floor(index))
	hi := int(math.Ceil(index))

	return float64(a[lo]) +
		(float64(a[hi])-float64(a[lo]))*
			(index-float64(lo))
}

func Evaluate(
	cs []Case,
	runs []Capture,
	judgments []Judgment,
) (Summary, error) {
	s := Summary{Total: len(cs)}

	cm := map[string]Case{}
	for _, c := range cs {
		cm[c.ID] = c
	}

	rm := map[string]Capture{}

	var durations []int64

	for _, r := range runs {
		if _, ok := cm[r.CaseID]; !ok {
			return s, fmt.Errorf(
				"unknown run case: %s",
				r.CaseID,
			)
		}

		if _, dup := rm[r.CaseID]; dup {
			return s, fmt.Errorf(
				"duplicate capture: %s",
				r.CaseID,
			)
		}

		rm[r.CaseID] = r

		if r.DurationMS > 0 &&
			r.Report != "" &&
			r.Error == "" {
			durations = append(
				durations,
				r.DurationMS,
			)
		}
	}

	s.Captured = len(rm)

	s.P50MS = Percentile(durations, .5)
	s.P95MS = Percentile(durations, .95)

	seen := map[string]bool{}

	for _, j := range judgments {
		c, exists := cm[j.CaseID]
		r, captured := rm[j.CaseID]

		if !exists ||
			!captured ||
			seen[j.CaseID] {
			return s, fmt.Errorf(
				"unknown, uncaptured or duplicate judgment: %s",
				j.CaseID,
			)
		}

		seen[j.CaseID] = true

		if j.RunID != r.RunID {
			return s, fmt.Errorf(
				"stale judgment run_id for %s",
				j.CaseID,
			)
		}

		// Missing human labels are not successes.
		if j.ResultCorrect == nil ||
			j.ReportedBug == nil ||
			(c.HasBug && j.EvidenceGrounded == nil) {
			continue
		}

		if r.Report == "" || r.Error != "" {
			continue
		}

		s.Graded++

		if *j.ResultCorrect {
			s.Correct++
		}

		if c.HasBug {
			s.BugGraded++
			if *j.EvidenceGrounded {
				s.EvidenceCorrect++
			}
		} else {
			s.CleanGraded++
			if *j.ReportedBug {
				s.FalsePositives++
			}
		}
	}

	return s, nil
}

func ReportMarkdown(s Summary) string {
	rate := func(a, b int) string {
		if b == 0 {
			return "N/A"
		}

		return fmt.Sprintf(
			"%.1f%% (%d/%d)",
			100*float64(a)/float64(b),
			a,
			b,
		)
	}

	return fmt.Sprintf(
		`# Day 16 Evaluation Report

- Manifest cases: %d
- Captured: %d
- Human graded: %d
- Grading coverage: %s
- Diagnosis correctness (human-reviewed): %s
- Evidence grounded (bug cases): %s
- False-positive rate (clean cases): %s
- Agent latency P50/P95 (captured samples): %.0f / %.0f ms

**Caveats:** No automatic root-cause verification. Ungraded results are excluded from accuracy; always report coverage. No token, RPM or cost claim without provider usage data. P50/P95 here measure CLI wall time, not pure model latency.
`,
		s.Total,
		s.Captured,
		s.Graded,
		rate(s.Graded, s.Total),
		rate(s.Correct, s.Graded),
		rate(s.EvidenceCorrect, s.BugGraded),
		rate(s.FalsePositives, s.CleanGraded),
		s.P50MS,
		s.P95MS,
	)
}
