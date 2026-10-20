# Day 21 evaluation

[English](evaluation.md) | [简体中文](zh-CN/evaluation.md) | [Home](../README.md)

## Evidence layers after the review

The [current results page](review-20261020/RESULTS.md) separates review regression, this historical Mock capture and the archived live single-file repair. The structured diagnosis-v1 gate checks observed execution and references, not root-cause correctness. Neither a 260-event regression count nor a passing diagnostic verdict is a live-model accuracy denominator. The recorded Day21 data below are unchanged.

## What was measured

The committed dataset is a recorded, deterministic Mock experiment:
four controlled scenarios, three trials each, using trusted single-file Go fixtures.
It is not a live DeepSeek evaluation or an open-world bug benchmark.

| Scenario / case_id | Expected behavior | Recorded checks |
| --- | --- | ---: |
| divide-repair | A failing baseline is repaired; independent Go tests pass; checkpoint succeeds | 3/3 |
| baseline-pass | An already-passing baseline consumes no repair round or model call | 3/3 |
| two-round-exhaust | Two unsuccessful rounds exhaust the budget without claiming success | 3/3 |
| verified-pass-takeover | A new real claim reuses a verified result with no new model call | 3/3 |

The Capture harness is TestDay21Capture in durable/day21_capture_test.go.
It uses real PostgreSQL, Queue.ClaimBatch, Scheduler.execute, CodingHandler and the
repair19 Patch Store. Proposals come from day21Proposer, not an external API.

## Recorded results

| Metric | Value | Denominator / interpretation |
| --- | ---: | --- |
| Scenario correctness | 12/12 | All four kinds must satisfy their own expected behavior |
| Fresh repair | 3/3 | Only divide-repair trials |
| Verified-result recovery | 3/3 | Only verified-pass-takeover trials |
| Unsupported success claims | 0/9 | Only statuses repaired or already_passed; excludes three expected exhausted runs |
| P50 | 400 ms | Nearest-rank percentile over the 12 recorded durations |
| P95 | 1403 ms | With 12 samples, the nearest-rank P95 is the largest sample |
| Durable repair rounds | 12 | Sum of rounds_used |
| Mock proposer calls | 9 | Sum of model_calls; not a live-model usage measurement |
| Token usage known | 0/12 | The zero token accumulators do not imply measured zero cost |

These numbers belong to the committed recorded dataset. A fresh capture reports
its own timing; it never overwrites these figures or claims they are portable benchmarks.

## Auditable files and scoring

- [cases.json](../evaldata/day21/cases.json): four kinds and exact trial counts.
- [runs.jsonl](../evaldata/day21/runs.jsonl): 12 captured Observation records.
- [summary.json](../evaldata/day21/summary.json): machine-readable aggregate.
- [report.md](../evaldata/day21/report.md): generated narrative report.
- [Scorer](../eval21/score.go) and [CLI](../cmd/day21-eval/main.go).

Observation fields are case_id, trial, run_id, status, baseline_failed, tests_passed,
original_clean, checkpoint_succeeded, recovered, rounds_used, attempts, model_calls,
usage_known, prompt_tokens, completion_tokens and duration_ms.
Records contain synthetic test Run IDs; no repository paths or contact details
were present, so the selected observations require no redaction. Capture logs,
local Worktrees and credentials are excluded from Git.

Score rejects unknown cases, missing/duplicate trials, reused Run IDs and invalid
numeric fields. The CLI rejects unknown observation fields and trailing JSON.
-require-all returns a nonzero exit code unless every scenario passes.

A positive claim requires independent tests_passed, original_clean and
checkpoint_succeeded evidence; repaired claims also require baseline_failed.
Scenario correctness applies additional kind-specific rules: recovery needs
recovered=true, model_calls=0 and attempts>=2; baseline-pass needs zero rounds;
budget exhaustion needs failed tests, no successful checkpoint and the exact
configured maximum rounds. These checks are bounded evidence for these fixtures,
not proof of arbitrary code correctness.

## Capture path and timing boundary

Fresh repair starts timing immediately before the actual claim and stops when
Scheduler.execute returns after final checkpoint handling. Independent proof
collection runs afterward and is excluded. For recovery, Worker A's preparation
of verified_pass is also excluded; the measured interval begins before Worker B's
new claim. Tests expire only their own Run; the new Epoch comes from ClaimBatch.

Day 21 recovery seeds a verified result and performs takeover. It does not kill a
Worker. The real Day 20 SIGKILL experiment is separate; the Day 19 Rename gap is
another boundary. See [crash recovery](crash-recovery.md).

## Reproduce the recorded report

This does not run the agent or need a database. Use Go and the checked-in files:

```bash
output="$(mktemp -d)"
env -u DEEPSEEK_API_KEY go run ./cmd/day21-eval   -cases evaldata/day21/cases.json -runs evaldata/day21/runs.jsonl   -report "$output/report.md" -summary "$output/summary.json" -require-all
cmp evaldata/day21/summary.json "$output/summary.json"
cmp evaldata/day21/report.md "$output/report.md"
```

## Capture a fresh experiment

After the dedicated database setup in [README](../README.md):

```bash
./scripts/day21_accept.sh
```

The runner checks both actual database names, unsets the real model key,
runs scorer/CLI unit tests, captures all 12 required subtests and checks the
committed report before scoring the new data. A fresh external directory owns
all output. DAY21_OUTPUT_DIR may name a new directory outside the repository;
an existing directory or repository-local path is rejected. Full ordinary/race
regression remains in test.sh and the CI jobs.

## Limits

The sample is small and repeated on controlled fixtures. Execution order,
compiler caches and host load affect latency. The recorded capture does not
contain a complete CPU/RAM/environment trace. Release validation uses Linux/WSL,
Go 1.27.1 and PostgreSQL 16, but that does not make 400/1403 ms a general claim.

No live-model success rate, API spending, throughput, sandbox security,
multi-host reconstruction or arbitrary-effect exactly-once conclusion follows.
