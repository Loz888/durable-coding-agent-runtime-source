# 实验结果、改进与证据范围 / Results and evidence scope

日期：2026-10-20。此页汇总已记录的实验，不覆盖历史原始数据。实现细节见 [IMPLEMENTATION](IMPLEMENTATION.md)，下一轮见[改进路线](../pilot/IMPROVEMENT_OUTLINE.zh-CN.md)。

| 验证对象 | 实际结果 | 可以支持的结论 | 证据 |
|---|---|---|---|
| 本轮完整回归 / race | 各 260 PASS，0 FAIL、0 SKIP | 当前受测实现的功能与竞态回归通过；计数含父测试和子测试 | [本地机器记录](../../reports/review-20261020/verification.json) |
| 独立测试、格式、vet、build | 138 PASS；其余检查 PASS | 数据库无关入口及静态检查通过 | 同上 |
| 远端 CI，645c88e | 全部验收 job 通过 | 该提交的 PG 集成、race、Mock/真实 SIGKILL 演示、Day21 Capture 与报告重算通过 | [Actions](https://github.com/Loz888/durable-coding-agent-runtime-source/actions/runs/39413383698)、[精简记录](../../reports/review-20261020/remote-ci.json) |
| Day21 历史 Mock | 场景正确性 12/12；新修复 3/3；恢复 3/3 | 受控夹具的确定性协议行为；不是模型成功率 | [原始报告](../../evaldata/day21/report.md) |
| 历史真实 DeepSeek 单文件修复 | 一轮补丁后测试通过；274 输入 / 94 输出 Token | 该历史样本完成过真实模型修复 | [归档证据](../../evaldata/historical/day18-deepseek/README.md) |

历史真实样本没有完整请求配置快照，不能归因于当前 High / Max，也不能推广成通用修复率。Day21 的 400/1403 ms 是该次 12 条记录的 P50/P95，不是新提交的性能或 SLA。

## 实验发现如何落实到实现

| 发现 / 风险 | 当前改进 | 验证与限制 |
|---|---|---|
| 终态时长会随读取时间增长 | 受保护 Checkpoint 事务冻结 finished_at；完成、运行中、历史缺失时间分开统计 | 两种 Store、终态重读、旧 CAS 拒绝与事务回滚测试；历史缺失时间不补造 |
| 流程完成被误当诊断正确 | 执行状态与 outcome_verdict 分开，检查测试执行元数据及引用摘要 | 一致、缺证据、伪造引用与冲突观察回归；不证明自由文本根因 |
| 两个适配器对模型错误处理不一致 | 共用错误分类、重试延迟与可能计费契约 | 本地 HTTP 故障注入及 Scheduler 决策对照；不是新增厂商质量测验 |
| 旧实验脚本与现行协议不一致 | Day15 入口明确退出，原文作为历史文本归档 | 不再把旧脚本当当前验收，也没有删除原实验 |

## 版本、迁移与未完成项

本地源码清单是[测试时内容快照](source-manifest.json)，远端记录分别绑定其实际 head SHA；不能把基线 SHA 或旧 CI 绿色状态当作后续提交的验证。迁移 009 是新增 outcome_verdict / diagnostic_report 列，启动新 Worker/API 前先应用；旧数据保持 not_evaluated。

R1 逻辑模型结果复用与未知结果崩溃矩阵、R2 最后一次领取后的专门收尾、R4 完整验证输入摘要仍待下一轮。实际容器隔离仍 BLOCKED；只支持受控单文件和原有效本地 Worktree，不声明任意仓库安全、业务正确、Exactly-once 或跨主机恢复。

本轮没有新增付费调用；后续配置 High，原预算不重置。该模型配置不是一份新增 Live 测验结果。

## English summary

The review passes 260 full and 260 race test/subtest events, plus 138 database-independent events. Remote CI at 645c88e also passes the existing crash and capture entrypoints. Terminal timestamps, diagnostic evidence verdicts and provider error handling were improved. The 12-trial Mock dataset and the one historical live repair remain separate evidence; neither establishes general repair accuracy. Logical-call reuse, last-claim finalization, complete verification fingerprints and actual container isolation are still pending.
