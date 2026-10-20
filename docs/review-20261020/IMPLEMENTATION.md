# Runtime：系统评审对照与本轮实现

日期：2026-10-20。评审文件为《两个Agent项目_系统评审与改进路线.md》。原评审基于更早快照；本轮从已发布 7c9cf79 继续，不重写 Queue、Scheduler、Lease 或 Patch Store。按报告“每轮验收后停止扩大范围”的顺序，完成第一轮核心问题与诊断证据闸门。

## 已落实

| 报告项 | 实际改动与验证 |
|---|---|
| 3.1 / A1 完成时间 | PostgresStore/FencedStore 在终态 Checkpoint 同一事务冻结 finished_at；成功/失败、重复 CAS、Checkpoint 插入失败回滚均有真实 PG 测试 |
| 3.1 统计口径 | MeanCompletedSeconds、MeanRunningSeconds 分开；CompletedMeasurements 给出分母，UnknownTerminalTimes 显式统计缺失历史时间。MeanRunSeconds 保留兼容，未知终态不再随当前时钟增长 |
| 3.2 / A3 诊断成功含义 | status 继续表示执行完成；outcome_verdict 单独记录。新增 diagnosis-v1 报告，引用当前 Run 的 call_id 和服务器输出摘要，核对最近测试的真实执行/退出状态 |
| 3.3 / A2 模型故障 | 近期代码已经有修复 HTTP 类型错误；本轮进一步统一 Category/Temporary/MayHaveCharged/RetryAfter 契约。401/429/503、坏 JSON、截断、响应头前后超时都比较两个适配器与 Scheduler 的实际决策 |
| 3.4 旧脚本脱节 | 原 Day15 脚本逐字节归档为文本，旧命令明确退出而不执行旧 SQL。当前 Mock/真实 ClaimBatch SIGKILL 演示继续保留，不冒充已重新接入 permanent/transient/hold 故障钩子 |

### 诊断结论的边界

- `succeeded` 表示流程完成，不等于诊断根因正确。
- `evidence_consistent` 只说明结构化测试观察和引用完整性一致；不证明自由文本结论、根因或任意代码正确。
- 未执行测试、旧结果没有执行元数据、未引用最后一次测试：`insufficient_evidence`。
- 报告通过而实际失败、引用伪造/其他结果、摘要不符：`inconsistent_evidence`。
- 编码修复路径另用 `tests_passed/tests_failed`，不能把测试通过当任意业务正确。
- 摘要绑定持久化的 UTF-8 工具文本，包含非 UTF-8 或被截断的日志也有往返一致性测试。这里仍是 Checkpoint 内的有界证据，不是完整 Artifact 生命周期平台。
- 当前 Run API 保留 status 并新增 execution_status/outcome_verdict；不会因此公开原始 History、源码路径或供应商思维内容。

## 数据库与升级

先执行 `scripts/setup_test_db.sh` 应用新增 009_outcome_verdict.sql，再启动新 Worker/API。迁移是新增列，旧记录默认 not_evaluated，不追认为已验证；旧 Worker 只写旧列时也不会获得新的验证结论。不要在生产 API 角色上运行测试管理员操作。

完成时长从 first_claimed_at 开始，不含初次领取前的排队等待，但包含之后的重试/接手等待；运行中统计不是 Worker CPU 或实际占用时长。不能把它称为提交到完成的 E2E。历史缺失时间不补造测量值，空平均的零占位须与 CompletedMeasurements/UnknownTerminalTimes 一起解释。

## 当前验收

[机器记录](../../reports/review-20261020/verification.json)：普通全回归 **260 PASS / 0 FAIL / 0 SKIP**，race 同为 **260/0/0**；独立测试 138 PASS，格式、vet 和 build 通过。计数含 Go 父测试/子测试事件。全部在本轮隔离 PG16 专用测试库运行，不修改原数据库，不调用真实模型。

最终代码对应[测试前源码哈希](source-manifest.json)。Mock/崩溃演示与 Day21 Capture 在本轮前一验证快照通过；最终提交的远端 CI 会再次运行其固定入口。历史真实单样本与 Day21 记录均保留，本轮不产生新的真实模型质量结论。

## 已有能力复用与剩余范围

- R3 / A8–A10：复用已有身份、登记仓库、幂等、取消、受限 API、execution profile 与共享模型准入；不新增另一套队列。
- R1：已有每尝试费用预留和未知成本保留；logical_call_id 结果复用、三个真实 SIGKILL 窗口还需下一轮专项验证，不能声称供应商 Exactly-once。
- R2：最后一次 Claim 后已 verified_pass 的专门收尾策略仍待实现和故障测试。
- R4：完整源码树/测试/依赖/工具链的 verification_input_digest 尚未收口；当前缓存仍限可信受控 Worktree。
- R5：已有容器适配代码，但无引擎，实际隔离仍 BLOCKED；不能以本次回归替代不可信代码沙箱验收。
- R6–R9：通用工具重放策略、Artifact GC、无进展检测、多文件/多主机恢复继续列为增量工作，不把建议写成已实现。

下一轮优先 R1/R2/R4；MCP、多 Agent、向量库和跨项目联动不在本轮扩展。

## English summary

This review round freezes terminal completion timestamps transactionally, separates completed/running/unknown duration metrics, unifies both provider failure contracts and adds structured diagnostic observation/reference checks. Execution completion is distinct from evidence consistency and from repair-test outcomes. The obsolete Day15 experiment is preserved verbatim as historical text. Full and race verification each pass 260 test/subtest events. Real-model quality, complete verification fingerprints and actual sandbox security are not inferred from these checks.
