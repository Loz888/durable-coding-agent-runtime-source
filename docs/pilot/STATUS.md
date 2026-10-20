# 当前实现与验收 / Current implementation and evidence

> 后续系统评审修复与最新验收见 [review implementation](../review-20261020/IMPLEMENTATION.md)。本文保留试点升级快照范围。

更新日期：2026-10-20。此页区分历史真实模型样本、确定性评测与当前试点代码；不将其合并为同一成功率。

## 已完成成果

- PostgreSQL Queue、Scheduler、Lease/Epoch、Revision CAS、Worktree 身份和四状态 Patch 恢复保留原实现。
- Day21 受控 Mock：12/12 场景正确，新修复 3/3，已验证结果恢复 3/3；[原始报告](../../evaldata/day21/report.md)不覆盖。
- 已找到并脱敏归档[历史真实 DeepSeek 修复](../../evaldata/historical/day18-deepseek/README.md)：一轮修复通过，274 输入 / 94 输出 Token。无请求快照，不能归因于当前模式。
- 最新试点实现包含摘要 Token、主体/团队权限、登记仓库、幂等提交、取消、查询，以及独立 execution profile，复用原 Queue/Scheduler。
- 跨 Worker 模型许可、持久费用预留、未知 Usage 保留、输入摘要与有界日志已实现；默认付费开关关闭。
- 当前后续模型配置为 `deepseek-flash`、thinking enabled、High。历史模型记录不改写，既有授权上限不重置。

## 本次发布验证

[机器记录](../../reports/pilot/github-update-20261020.json)与[源码内容清单](source-manifest.json)记录实际验证范围。普通与 race 回归各 **234 PASS / 0 FAIL / 0 SKIP**；独立测试 134 PASS，vet、构建、格式、Mock/崩溃演示与 Day21 Capture 通过。验证包含 TCP/SCRAM 与独立受限 API 登录。测试在新建 PostgreSQL16 集群的两个专用数据库运行，不改原数据库，不调用真实模型。源码清单绑定文件内容；基线 SHA 不是未提交改动的完整身份。

## 明确边界

- 可信本机执行路径会执行仓库代码；`executor/sandbox_linux.go` 已有容器适配实现，但本机无引擎，实际容器隔离验收仍 BLOCKED。
- 完整候选树与缓存结论绑定、进程死亡后的孤儿容器回收还需收口；不能接收任意不可信仓库并声称安全。
- 当前受控修复针对配置好的单文件；不声明任意多文件开发、外部副作用 Exactly-once、生产 SLA 或多主机 Worktree 恢复。
- 数据库 Lease 仍依赖数据库时钟。当前回归通过不消除时钟跳变和环境调度风险。
- 试点 API 使用受限数据库角色；集成测试的隔离管理员需有创建/管理测试角色的权限。不能把生产 API 角色提权来跑测试。

后续仅针对缺口推进：[改进大纲](IMPROVEMENT_OUTLINE.zh-CN.md)。历史真实样本已经完成，不再笼统标为“从未调用模型”。

## English summary

The existing durable execution and recovery protocol is retained. Pilot code adds scoped API identities, idempotency, cancellation, execution-profile separation and shared model admission/accounting. A sanitized historical live DeepSeek repair is now available separately from the 12-trial deterministic Day21 evaluation. Its exact model snapshot/effort is unknown. Current configured effort is High. Container isolation is implemented as an adapter but remains unverified without an engine; single-file, local-worktree and trusted-control-plane limitations still apply. See the machine report and content manifest for this publication's actual checks.
