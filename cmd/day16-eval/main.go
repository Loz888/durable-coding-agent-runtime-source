package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"runtime-app/eval16"
)

func main() {
	mode := flag.String(
		"action",
		"",
		"capture | template | score",
	)

	casesPath := flag.String(
		"cases",
		"evaldata/cases.json",
		"ground truth manifest",
	)

	runsPath := flag.String(
		"runs",
		"evaldata/runs.jsonl",
		"captured Agent reports",
	)

	judgesPath := flag.String(
		"judgments",
		"evaldata/judgments.jsonl",
		"manual judgments",
	)

	reportPath := flag.String(
		"report",
		"evaldata/report.md",
		"Markdown report",
	)

	binary := flag.String(
		"bin",
		"",
		"existing Agent executable",
	)

	agentMode := flag.String(
		"agent-mode",
		"demo",
		"demo or deepseek",
	)

	timeout := flag.Duration(
		"timeout",
		120*time.Second,
		"timeout per Agent run",
	)

	flag.Parse()

	cs, err := eval16.LoadCases(*casesPath)
	must(err)

	switch *mode {

	case "capture":
		if *binary == "" {
			must(fmt.Errorf("-bin is required"))
		}

		goal := "请先运行真实Go测试，读取相关源码，根据测试证据定位根因并提出修复建议。若测试通过，不要编造缺陷。输出中文诊断报告。"

		var runs []eval16.Capture

		for _, c := range cs {
			fmt.Printf(
				"CAPTURE %s (%s)\n",
				c.ID,
				c.Category,
			)

			r := eval16.CaptureCase(
				context.Background(),
				c,
				eval16.CaptureOptions{
					Binary:        binaryValue(binary),
					Mode:          *agentMode,
					Goal:          goal,
					OracleTimeout: 40 * time.Second,
					AgentTimeout:  *timeout,
				},
			)

			if r.Error != "" {
				fmt.Fprintf(
					os.Stderr,
					"  WARNING: %s\n",
					r.Error,
				)
			}

			runs = append(runs, r)
		}

		must(eval16.WriteJSONL(
			*runsPath,
			runs,
		))

		fmt.Printf(
			"saved %d capture records to %s\n",
			len(runs),
			*runsPath,
		)

	case "template":
		runs, err := eval16.ReadJSONL[eval16.Capture](*runsPath)
		must(err)

		var judgments []eval16.Judgment

		for _, r := range runs {
			judgments = append(
				judgments,
				eval16.Judgment{
					CaseID: r.CaseID,
					RunID:  r.RunID,
					Notes:  "待人工阅读报告、源码与实际测试日志后评分",
				},
			)
		}

		must(eval16.WriteJSONL(
			*judgesPath,
			judgments,
		))

		fmt.Printf(
			"created %d BLANK judgments at %s\n",
			len(judgments),
			*judgesPath,
		)

	case "score":
		runs, err := eval16.ReadJSONL[eval16.Capture](*runsPath)
		must(err)

		judgments, err := eval16.ReadJSONL[eval16.Judgment](*judgesPath)
		must(err)

		s, err := eval16.Evaluate(
			cs,
			runs,
			judgments,
		)
		must(err)

		report := eval16.ReportMarkdown(s)

		must(os.WriteFile(
			*reportPath,
			[]byte(report),
			0644,
		))

		fmt.Print(report)

	default:
		must(fmt.Errorf(
			"-action must be capture, template or score",
		))
	}
}

func binaryValue(s *string) string {
	return *s
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(
			os.Stderr,
			"Day16:",
			err,
		)
		os.Exit(1)
	}
}
