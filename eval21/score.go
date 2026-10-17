package eval21

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

type Case struct {
	ID        string `json:"case_id"`
	Kind      string `json:"kind"`
	Trials    int    `json:"trials"`
	MaxRounds int    `json:"max_rounds,omitempty"`
}

type Manifest struct {
	Cases []Case `json:"cases"`
}

// DurationMS: ClaimBatch -> final Checkpoint.
// Independent proof checks are excluded.
type Observation struct {
	CaseID              string `json:"case_id"`
	Trial               int    `json:"trial"`
	RunID               string `json:"run_id"`
	Status              string `json:"status"`
	BaselineFailed      bool   `json:"baseline_failed"`
	TestsPassed         bool   `json:"tests_passed"`
	OriginalClean       bool   `json:"original_clean"`
	CheckpointSucceeded bool   `json:"checkpoint_succeeded"`
	Recovered           bool   `json:"recovered"`
	RoundsUsed          int    `json:"rounds_used"`
	Attempts            int    `json:"attempts"`
	ModelCalls          int    `json:"model_calls"`
	UsageKnown          bool   `json:"usage_known"`
	PromptTokens        int    `json:"prompt_tokens"`
	CompletionTokens    int    `json:"completion_tokens"`
	DurationMS          int64  `json:"duration_ms"`
}

type CaseResult struct {
	CaseID string `json:"case_id"`
	Kind   string `json:"kind"`
	Pass   int    `json:"pass"`
	Total  int    `json:"total"`
}

type Report struct {
	TotalRuns       int          `json:"total_runs"`
	ScenarioPassed  int          `json:"scenario_passed"`
	RepairPassed    int          `json:"repair_passed"`
	RepairTotal     int          `json:"repair_total"`
	RecoveryPassed  int          `json:"recovery_passed"`
	RecoveryTotal   int          `json:"recovery_total"`
	FalseClaims     int          `json:"false_claims"`
	PositiveClaims  int          `json:"positive_claims"`
	TotalRounds     int          `json:"total_rounds"`
	TotalModelCalls int          `json:"total_model_calls"`
	TokenKnownRuns  int          `json:"token_known_runs"`
	PromptTokens    int          `json:"prompt_tokens"`
	OutputTokens    int          `json:"completion_tokens"`
	P50LatencyMS    int64        `json:"p50_latency_ms"`
	P95LatencyMS    int64        `json:"p95_latency_ms"`
	Cases           []CaseResult `json:"cases"`
}

func Score(
	m Manifest,
	runs []Observation,
) (Report, error) {
	var r Report

	if len(m.Cases) == 0 {
		return r, errors.New("empty manifest")
	}

	caseMap := map[string]Case{}
	counts := map[string]int{}
	seen := map[string]bool{}
	seenRuns := map[string]bool{}
	passes := map[string]int{}

	latencies := make([]int64, 0, len(runs))

	for _, c := range m.Cases {
		if c.ID == "" ||
			c.Trials < 1 ||
			c.Trials > 100 ||
			c.MaxRounds < 0 {
			return r, fmt.Errorf(
				"invalid case %+v", c,
			)
		}

		switch c.Kind {
		case "repair", "recovery",
			"already_passed", "budget_exhausted":
		default:
			return r, fmt.Errorf(
				"invalid kind %q", c.Kind,
			)
		}

		if _, ok := caseMap[c.ID]; ok {
			return r, fmt.Errorf(
				"duplicate case %s", c.ID,
			)
		}

		caseMap[c.ID] = c
	}

	for _, o := range runs {
		c, ok := caseMap[o.CaseID]
		if !ok {
			return r, fmt.Errorf(
				"unknown case %q", o.CaseID,
			)
		}

		if o.RunID == "" ||
			o.Trial < 1 ||
			o.Trial > c.Trials ||
			o.DurationMS < 0 ||
			o.RoundsUsed < 0 ||
			o.Attempts < 1 ||
			o.ModelCalls < 0 ||
			o.PromptTokens < 0 ||
			o.CompletionTokens < 0 {
			return r, fmt.Errorf(
				"invalid observation %+v", o,
			)
		}

		key := fmt.Sprintf(
			"%s/%d", o.CaseID, o.Trial,
		)

		if seen[key] || seenRuns[o.RunID] {
			return r, fmt.Errorf(
				"duplicate trial/run %q", key,
			)
		}

		seen[key] = true
		seenRuns[o.RunID] = true
		counts[o.CaseID]++

		goodEvidence :=
			o.TestsPassed &&
				o.OriginalClean &&
				o.CheckpointSucceeded

		var pass bool

		switch c.Kind {
		case "repair":
			pass = o.Status == "repaired" &&
				o.BaselineFailed &&
				goodEvidence

			r.RepairTotal++
			if pass {
				r.RepairPassed++
			}

		case "recovery":
			pass = o.Status == "repaired" &&
				o.BaselineFailed &&
				goodEvidence &&
				o.Recovered &&
				o.ModelCalls == 0 &&
				o.Attempts >= 2

			r.RecoveryTotal++
			if pass {
				r.RecoveryPassed++
			}

		case "already_passed":
			pass = o.Status == "already_passed" &&
				!o.BaselineFailed &&
				goodEvidence &&
				o.ModelCalls == 0 &&
				o.RoundsUsed == 0

		case "budget_exhausted":
			pass = o.Status == "exhausted" &&
				o.BaselineFailed &&
				!o.TestsPassed &&
				o.OriginalClean &&
				!o.CheckpointSucceeded &&
				c.MaxRounds > 0 &&
				o.RoundsUsed == c.MaxRounds
		}

		if pass {
			r.ScenarioPassed++
			passes[o.CaseID]++
		}

		if o.Status == "repaired" ||
			o.Status == "already_passed" {
			r.PositiveClaims++

			if !goodEvidence ||
				(o.Status == "repaired" &&
					!o.BaselineFailed) {
				r.FalseClaims++
			}
		}

		r.TotalRounds += o.RoundsUsed
		r.TotalModelCalls += o.ModelCalls

		if o.UsageKnown {
			r.TokenKnownRuns++
			r.PromptTokens += o.PromptTokens
			r.OutputTokens += o.CompletionTokens
		}

		latencies = append(
			latencies,
			o.DurationMS,
		)
	}

	for _, c := range m.Cases {
		if counts[c.ID] != c.Trials {
			return Report{}, fmt.Errorf(
				"case %s: got %d of %d trials",
				c.ID,
				counts[c.ID],
				c.Trials,
			)
		}

		r.Cases = append(
			r.Cases,
			CaseResult{
				CaseID: c.ID,
				Kind:   c.Kind,
				Pass:   passes[c.ID],
				Total:  c.Trials,
			},
		)
	}

	r.TotalRuns = len(runs)

	sort.Slice(
		latencies,
		func(i, j int) bool {
			return latencies[i] < latencies[j]
		},
	)

	r.P50LatencyMS = percentile(
		latencies, .50,
	)

	r.P95LatencyMS = percentile(
		latencies, .95,
	)

	return r, nil
}

func percentile(
	v []int64,
	p float64,
) int64 {
	if len(v) == 0 {
		return 0
	}

	return v[int(math.Ceil(float64(len(v))*p))-1]
}

func (r Report) Markdown() string {
	var b strings.Builder

	fmt.Fprintln(
		&b,
		"# Day 21 · Coding Agent Evaluation",
	)

	fmt.Fprintln(&b, "\n## Methodology")

	fmt.Fprintln(
		&b,
		"- Dataset: deterministic, trusted Go fixtures; "+
			"not an independent real-world repair benchmark.",
	)

	fmt.Fprintln(
		&b,
		"- Latency: ClaimBatch to final checkpoint; "+
			"independent proof checks excluded.",
	)

	fmt.Fprintln(
		&b,
		"- A passing Go test is evidence for the stated "+
			"fixture, not proof of arbitrary correctness.",
	)

	fmt.Fprintln(
		&b,
		"- Mock calls are not real DeepSeek token measurements. "+
			"Unknown usage is not assumed zero.",
	)

	fmt.Fprintf(
		&b,
		"\n## Summary\n\n"+
			"- Scenario correctness: **%d/%d**\n"+
			"- Fresh repair success: **%d/%d**\n"+
			"- Verified-result recovery: **%d/%d**\n",
		r.ScenarioPassed,
		r.TotalRuns,
		r.RepairPassed,
		r.RepairTotal,
		r.RecoveryPassed,
		r.RecoveryTotal,
	)

	fmt.Fprintf(
		&b,
		"- False positive success claims: **%d/%d** "+
			"(claimed successes only)\n",
		r.FalseClaims,
		r.PositiveClaims,
	)

	fmt.Fprintf(
		&b,
		"- P50 / P95 end-to-end latency: **%d / %d ms**\n",
		r.P50LatencyMS,
		r.P95LatencyMS,
	)

	fmt.Fprintf(
		&b,
		"- Total durable repair rounds: **%d**, "+
			"model calls: **%d**\n",
		r.TotalRounds,
		r.TotalModelCalls,
	)

	fmt.Fprintf(
		&b,
		"- Token usage available for **%d/%d** runs; "+
			"known input/output tokens: **%d/%d**\n",
		r.TokenKnownRuns,
		r.TotalRuns,
		r.PromptTokens,
		r.OutputTokens,
	)

	fmt.Fprintln(
		&b,
		"\n## Per-case results\n\n"+
			"| Case | Kind | Passed | Trials |\n"+
			"|---|---|---:|---:|",
	)

	for _, c := range r.Cases {
		fmt.Fprintf(
			&b,
			"| %s | %s | %d | %d |\n",
			c.CaseID,
			c.Kind,
			c.Pass,
			c.Total,
		)
	}

	fmt.Fprintln(
		&b,
		"\n## Limitations\n\n"+
			"- No real-model success rate, API spending, "+
			"multi-host reliability, or throughput claim "+
			"is supported by this report.",
	)

	return b.String()
}

func WriteFiles(
	report Report,
	mdPath,
	jsonPath string,
) error {
	if err := os.WriteFile(
		mdPath,
		[]byte(report.Markdown()),
		0644,
	); err != nil {
		return err
	}

	b, err := json.MarshalIndent(
		report,
		"",
		"  ",
	)

	if err != nil {
		return err
	}

	return os.WriteFile(
		jsonPath,
		append(b, '\n'),
		0644,
	)
}
