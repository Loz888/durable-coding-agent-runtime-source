# Release validation

[English](validation.md) | [简体中文](zh-CN/validation.md) | [Home](../README.md)

## Latest review evidence · 2026-10-20

Full and race suites each pass 260 test/subtest events; database-independent suites pass 138, with no failed or skipped tests. CI at 645c88e passes the existing PostgreSQL, crash and Capture entrypoints. See [results and scope](review-20261020/RESULTS.md), [local evidence](../reports/review-20261020/verification.json) and [exact remote runs](../reports/review-20261020/remote-ci.json). No new coverage percentages or paid-model measurements are inferred from these checks.

## Historical release snapshot · 2026-10-17

Local acceptance was executed on 2026-10-17 in the independent release checkout
using Go 1.27.1, PostgreSQL 16 and Linux/WSL. No live model API key was provided.
Only the two fixed dedicated test databases were used.

| Validation | Passed test entries | FAIL | SKIP |
| --- | ---: | ---: | ---: |
| Full project | 226 | 0 | 0 |
| Full project race detector | 226 | 0 | 0 |
| Full project coverage run | 226 | 0 | 0 |
| repair20 / durable / repair19 repeated ten times | 690 | 0 | 0 |
| Database-independent suites | 133 | 0 | 0 |
| Mock demo | 1 | 0 | 0 |
| Four-state + Day20 crash demo | 6 | 0 | 0 |

Counts include parent tests, subtests and crash-child helper entrypoints.
Format, build and go vet also passed. Every counted suite rejected actual
test SKIP; packages with no tests were not misreported as skipped tests.

## Day 21 and reproduction

The acceptance runner captured 12 fresh deterministic trials and strict scoring
passed all scenarios. It also recomputed the committed summary and Markdown
report byte-for-byte. Original cases, observations and recorded 400/1403 ms
P50/P95 were preserved. The fresh capture's latency is a separate observation,
not a replacement benchmark.

The new Day 21 runner owns a fresh external output directory and does not delete
committed JSONL. [Evaluation](evaluation.md) gives the recorded data, real capture
path, denominators, timing boundary and sample limitations.

## Measured statement coverage

| Package | Coverage |
| --- | ---: |
| cmd/day21-eval | 55.6% |
| cmd/day8-server | 87.3% |
| distributed | 66.3% |
| durable | 81.2% |
| eval16 | 89.4% |
| eval21 | 94.9% |
| executor | 93.1% |
| httpapi | 58.2% |
| internal/testdb | 35.3% |
| observability | 93.9% |
| persistence | 44.9% |
| reliability15 | 85.7% |
| repair17 | 83.3% |
| repair18 | 82.5% |
| repair19 | 66.9% |
| repair20 | 39.2% |
| repodiag | 84.7% |
| runtimecore | 94.3% |

internal/integration20 is test-only and has no production statements.
Command packages without unit tests report zero/no test files. Coverage values
describe this recorded run and are not a performance or production-reliability measure.

## Remote CI and scope

[Actions](https://github.com/Loz888/durable-coding-agent-runtime-source/actions) is the
authoritative current remote status. CI prepares PostgreSQL 16 over TCP, creates
both dedicated databases, applies migrations 001–009, then runs full ordinary
and race suites, the real Mock/crash demos and Day 21 Capture/report reproduction.
Local WSL validation used the corresponding Unix-socket connection.
No local result alone is asserted as remote CI success.

The source repository is public; no software license has been selected.
The recorded local results above were obtained in the independent release
checkout. The same implementation and test files were copied into this public
repository without importing its private Git history. Original development
files were read-only. Local reports, raw logs, credentials and backups stay
outside Git. The public Actions link above reports this repository's own CI.

Day 19 Rename, Day 20 final-checkpoint SIGKILL and Day 21 seeded takeover are
distinct experiments. No sandbox, arbitrary-effect exactly-once or multi-host
Worktree recovery is claimed.
