# Day21 评测方法

[English](../evaluation.md) | [简体中文](evaluation.md) | [中文首页](../../README.zh-CN.md)

## 评审后证据分层

[当前结果汇总](../review-20261020/RESULTS.md)分别列出工程回归、本页历史 Mock 与真实单文件修复。新增 diagnosis-v1 闸门验证测试观察和引用一致性，不验证自由文本根因；260 个测试事件或 evidence_consistent 不是真实模型正确率分母。下方 Day21 原始数据不改。

## 评测对象

已提交数据来自受控、确定性的 Mock 实验：四类场景，每类三次，
输入是可信的单文件 Go 夹具，不是开放真实缺陷基准，也没有调用真实 DeepSeek。

| 场景 / case_id | 期望行为 | 已记录结果 |
| --- | --- | ---: |
| divide-repair | 失败基线被修复，独立 Go 测试通过，检查点成功 | 3/3 |
| baseline-pass | 基线已通过，不消耗修复轮次和模型调用 | 3/3 |
| two-round-exhaust | 两轮错误提案后预算耗尽，不宣称成功 | 3/3 |
| verified-pass-takeover | 新的真实领取复用已验证结果，不再调用模型 | 3/3 |

入口是 durable/day21_capture_test.go 中的 TestDay21Capture。
实际使用 PostgreSQL、Queue.ClaimBatch、Scheduler.execute、
CodingHandler 和 repair19 Patch Store；提案来自 day21Proposer。

## 指标与分母

| 指标 | 数值 | 解释 |
| --- | ---: | --- |
| 场景正确性 | 12/12 | 四类场景分别满足各自行为 |
| 新修复 | 3/3 | 只统计 divide-repair |
| 已验证结果恢复 | 3/3 | 只统计 verified-pass-takeover |
| 缺乏证据的成功声明 | 0/9 | 只统计 repaired 或 already_passed，排除预期失败的三次 exhausted |
| P50 | 400 ms | 12 条已记录耗时的最近秩分位数 |
| P95 | 1403 ms | 12 个样本的最近秩 P95 等于最大样本 |
| 持久化修复轮次 | 12 | rounds_used 总和 |
| Mock 提案调用 | 9 | model_calls 总和，不代表真实模型用量 |
| Token 用量已知 | 0/12 | Token 累加器为零，不代表测得零成本 |

这些数值属于已提交记录。新 Capture 产生本轮自己的耗时，
不会覆盖旧记录，也不能用历史延迟声称通用性能。

## 数据与评分规则

- [cases.json](../../evaldata/day21/cases.json)：四类场景及每类试验数。
- [runs.jsonl](../../evaldata/day21/runs.jsonl)：12 条 Observation。
- [summary.json](../../evaldata/day21/summary.json)：结构化汇总。
- [report.md](../../evaldata/day21/report.md)：自动生成的文字报告。
- [评分器](../../eval21/score.go)与[CLI](../../cmd/day21-eval/main.go)。

Observation 包含 case_id、trial、run_id、status、baseline_failed、
tests_passed、original_clean、checkpoint_succeeded、recovered、rounds_used、
attempts、model_calls、usage_known、prompt_tokens、completion_tokens、duration_ms。
记录仅包含合成测试 Run ID 和受限标量字段，没有路径、联系方式或凭据，
因此选定数据无需脱敏改写；原始 Capture 日志和临时 Worktree 不提交。

Score 拒绝未知场景、缺失或重复试验、复用 Run ID 和不合法数值。
CLI 拒绝观测中的未知字段和尾随 JSON；
-require-all 要求所有场景都符合预期，否则返回非零退出码。

成功声明需要 tests_passed、original_clean、checkpoint_succeeded 证据，
repaired 还要求 baseline_failed。
场景正确性进一步检查：恢复需要 recovered=true、model_calls=0、attempts>=2；
已通过基线需要零轮次；预算耗尽要求测试仍失败、检查点不成功，
且轮次正好等于设定上限。这些只是对应夹具的证据，不证明任意代码的正确性。

## 执行与计时边界

新修复在实际领取前开始计时，Scheduler.execute 完成检查点处理返回后停止。
独立证据核查不计入延迟。
恢复场景排除 Worker A 准备 verified_pass 的时间，从 Worker B 领取前开始计时。
测试只使自身 Run 的租约过期，新的 Epoch 仍由 ClaimBatch 生成。

Day21 恢复先保存验证结果再接管，没有终止进程。
Day20 的真实 SIGKILL 与 Day19 的 Rename 故障窗口属于独立实验，
详见[崩溃恢复](crash-recovery.md)。

## 重算已提交报告

只需 Go 与仓库数据，不需要数据库或模型：

```bash
output="$(mktemp -d)"
env -u DEEPSEEK_API_KEY go run ./cmd/day21-eval   -cases evaldata/day21/cases.json -runs evaldata/day21/runs.jsonl   -report "$output/report.md" -summary "$output/summary.json" -require-all
cmp evaldata/day21/summary.json "$output/summary.json"
cmp evaldata/day21/report.md "$output/report.md"
```

## 采集新实验

按[中文 README](../../README.zh-CN.md)配置专用测试库后执行：

```bash
./scripts/day21_accept.sh
```

脚本检查两个实际数据库名称、清除真实模型 Key、
执行评分器与 CLI 测试、检查全部 12 个必需 Capture 子场景，
重算已提交报告，再评分新采集的数据。

默认输出在新建的外部临时目录。DAY21_OUTPUT_DIR 可指定一个尚不存在的外部目录；
已有目录或仓库内路径会被拒绝。
完整普通测试与竞态测试由 test.sh 和 CI 分别执行。

## 局限

样本量小，输入受控，重复发生在同类夹具中。
运行顺序、编译缓存与主机负载都会影响延迟，
历史记录没有包含完整的 CPU、内存和环境轨迹。
本次发布验证使用 Linux/WSL、Go 1.27.1、PostgreSQL 16，
不意味着 400/1403 ms 可以推广到其他环境。

不能据此得出真实模型修复率、API 支出、吞吐量、沙箱安全、
跨主机重建或任意外部副作用恰好一次的结论。
