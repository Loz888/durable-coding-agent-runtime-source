package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDay21CLIReportAndStrictMode(
	t *testing.T,
) {
	d := t.TempDir()

	cases := filepath.Join(d, "cases.json")
	runs := filepath.Join(d, "runs.jsonl")

	md := filepath.Join(d, "out", "report.md")
	summary := filepath.Join(d, "out", "summary.json")

	if err := os.WriteFile(
		cases,
		[]byte(`{"cases":[{"case_id":"ok","kind":"already_passed","trials":1}]}`),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	good := `{"case_id":"ok","trial":1,"run_id":"run-1","status":"already_passed","tests_passed":true,"original_clean":true,"checkpoint_succeeded":true,"attempts":1,"duration_ms":12}` + "\n"

	if err := os.WriteFile(
		runs,
		[]byte(good),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	if err := execute(
		cases, runs, md, summary, true,
	); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(md)

	if err != nil ||
		!strings.Contains(
			string(data),
			"Scenario correctness: **1/1**",
		) {
		t.Fatalf(
			"report: %v %s",
			err, data,
		)
	}

	bad := strings.Replace(
		good,
		`"tests_passed":true`,
		`"tests_passed":false`,
		1,
	)

	if err := os.WriteFile(
		runs,
		[]byte(bad),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	if err := execute(
		cases, runs, md, summary, true,
	); err == nil {
		t.Fatal(
			"strict mode accepted failed case",
		)
	}
}

func TestDay21CLIRejectUnknownJSONField(
	t *testing.T,
) {
	d := t.TempDir()

	cases := filepath.Join(d, "cases.json")
	runs := filepath.Join(d, "runs.jsonl")

	if err := os.WriteFile(
		cases,
		[]byte(`{"cases":[{"case_id":"ok","kind":"repair","trials":1}]}`),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	raw := `{"case_id":"ok","trial":1,"run_id":"run-1","attempts":1,"unknown_field":8}` + "\n"

	if err := os.WriteFile(
		runs,
		[]byte(raw),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	if err := execute(
		cases,
		runs,
		filepath.Join(d, "r.md"),
		filepath.Join(d, "s.json"),
		false,
	); err == nil {
		t.Fatal(
			"unexpected field accepted",
		)
	}
}
