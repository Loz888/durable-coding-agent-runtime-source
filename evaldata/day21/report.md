# Day 21 · Coding Agent Evaluation

## Methodology
- Dataset: deterministic, trusted Go fixtures; not an independent real-world repair benchmark.
- Latency: ClaimBatch to final checkpoint; independent proof checks excluded.
- A passing Go test is evidence for the stated fixture, not proof of arbitrary correctness.
- Mock calls are not real DeepSeek token measurements. Unknown usage is not assumed zero.

## Summary

- Scenario correctness: **12/12**
- Fresh repair success: **3/3**
- Verified-result recovery: **3/3**
- False positive success claims: **0/9** (claimed successes only)
- P50 / P95 end-to-end latency: **400 / 1403 ms**
- Total durable repair rounds: **12**, model calls: **9**
- Token usage available for **0/12** runs; known input/output tokens: **0/0**

## Per-case results

| Case | Kind | Passed | Trials |
|---|---|---:|---:|
| divide-repair | repair | 3 | 3 |
| baseline-pass | already_passed | 3 | 3 |
| two-round-exhaust | budget_exhausted | 3 | 3 |
| verified-pass-takeover | recovery | 3 | 3 |

## Limitations

- No real-model success rate, API spending, multi-host reliability, or throughput claim is supported by this report.
