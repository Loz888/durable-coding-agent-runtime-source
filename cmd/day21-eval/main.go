package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"runtime-app/eval21"
)

func main() {
	casesPath := flag.String(
		"cases",
		"evaldata/day21/cases.json",
		"case manifest",
	)

	runsPath := flag.String(
		"runs",
		"evaldata/day21/runs.jsonl",
		"captured run observations",
	)

	mdPath := flag.String(
		"report",
		"evaldata/day21/report.md",
		"markdown report",
	)

	jsonPath := flag.String(
		"summary",
		"evaldata/day21/summary.json",
		"machine-readable report",
	)

	strict := flag.Bool(
		"require-all",
		false,
		"fail if any scenario does not meet expected behavior",
	)

	flag.Parse()

	if err := execute(
		*casesPath,
		*runsPath,
		*mdPath,
		*jsonPath,
		*strict,
	); err != nil {
		fmt.Fprintln(
			os.Stderr,
			"day21-eval:",
			err,
		)
		os.Exit(1)
	}
}

func execute(
	casesPath,
	runsPath,
	mdPath,
	jsonPath string,
	strict bool,
) error {
	f, err := os.Open(casesPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var m eval21.Manifest

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&m); err != nil {
		return fmt.Errorf(
			"manifest: %w", err,
		)
	}

	file, err := os.Open(runsPath)
	if err != nil {
		return err
	}
	defer file.Close()

	var observations []eval21.Observation

	s := bufio.NewScanner(file)
	s.Buffer(
		make([]byte, 4096),
		4*1024*1024,
	)

	for line := 1; s.Scan(); line++ {
		if len(s.Bytes()) == 0 {
			continue
		}

		var x eval21.Observation

		decoder := json.NewDecoder(
			bytes.NewReader(s.Bytes()),
		)
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&x); err != nil {
			return fmt.Errorf(
				"runs.jsonl:%d: %w",
				line, err,
			)
		}

		var extra any

		if err := decoder.Decode(&extra); err != io.EOF {
			return fmt.Errorf(
				"runs.jsonl:%d: trailing data",
				line,
			)
		}

		observations = append(
			observations, x,
		)
	}

	if err := s.Err(); err != nil {
		return err
	}

	r, err := eval21.Score(
		m, observations,
	)
	if err != nil {
		return err
	}

	for _, p := range []string{
		mdPath,
		jsonPath,
	} {
		if err := os.MkdirAll(
			filepath.Dir(p),
			0755,
		); err != nil {
			return err
		}
	}

	if err := eval21.WriteFiles(
		r, mdPath, jsonPath,
	); err != nil {
		return err
	}

	if strict &&
		r.ScenarioPassed != r.TotalRuns {
		return fmt.Errorf(
			"acceptance failed: scenario %d/%d",
			r.ScenarioPassed,
			r.TotalRuns,
		)
	}

	fmt.Printf(
		"Day21: scenario %d/%d, repair %d/%d, "+
			"recovery %d/%d; P95=%dms; report=%s\n",

		r.ScenarioPassed,
		r.TotalRuns,
		r.RepairPassed,
		r.RepairTotal,
		r.RecoveryPassed,
		r.RecoveryTotal,
		r.P95LatencyMS,
		mdPath,
	)

	return nil
}
