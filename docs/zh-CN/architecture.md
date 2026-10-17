# 系统架构

[English](../architecture.md) | [简体中文](architecture.md) | [中文首页](../../README.zh-CN.md)

本文介绍本仓库实现中的组件与边界。

## 组件职责与依赖关系

runtimecore 通过 executor.Registry 并发执行 task.Task，并收集执行结果。
persistence/httpapi 提供持久化任务 API。
这套内存任务运行时与持久化 Agent Run 调度是不同的层次。

durable 管理 Queue、LeaseManager、FencedStore 和 Scheduler。
诊断 Provider 使用 durable.Runner 与 repodiag；coding:* Provider 使用
CodingHandler 与 repair20.Agent。两条路径复用相同的领取、心跳和调度生命周期；
代码修复提案不会调用诊断模型工厂。

生产模块依赖为：durable → repair20/repair19；
repair20 → repair19/repair18/repair17；repair19 → repair17。
internal/integration20 仅用于测试，单向引用这些模块。
生产模块不导入它，也不需要为其额外暴露私有测试辅助函数。

## 数据库结构

实现按顺序应用数据库迁移：

| 迁移 | 主要变化 |
| --- | --- |
| 001 | 创建 day12.agent_runs/checkpoints |
| 002 | 增加租约持有者、Epoch 和到期时间 |
| 003 | 增加队列、优先级、可用时间与 retry_count |
| 004 | 增加预算、失败信息、时间字段与 run_events |
| 005 | 创建 day19.repair_runs 与 patch_ops |
| 006 | 增加 Worktree Identity 和表示因果顺序的 round_number |

HTTP 持久化通过 EnsureTaskSchema 管理独立的 tasks 结构；
测试在随机的 runtime_test_* schema 中应用 schema.sql。

生产事件以 (run_id, revision, event_type) 保证唯一。
旧 Day15 测试夹具使用另一种结构，由 Store 识别。
相同版本、相同类型的观测可能合并，因此不能将其理解为“每次尝试必有一条独立事件”。

## Worktree 生命周期

repair17.Prepare 创建分离 HEAD 的 Git Worktree，并写入 Run、基线信息与随机身份令牌。
repair19.Register 将其绝对路径、源码提交和身份绑定到逻辑 Run。
恢复时重新打开并验证同一份绑定。

工作区丢失、路径成为符号链接、身份被替换或基线被 rebase 时，校验会拒绝继续执行。
尚未完成的 Run 需要保留工作区以便恢复；
集成测试夹具负责自己创建的临时源码与清理工作。

## 持久化边界

短事务先锁定 agent_runs，再锁定 repair_runs/patch_ops。
Claim 与 Renew 竞争同一条 Run 记录。
受保护的 Rename 之后，还要验证执行权限与文件哈希，才能提交结果。

模型请求与 Go 测试在行锁事务之外运行。
该方案依赖可信写入者与共享的本地文件系统；
不提供沙箱，也不构成文件系统与数据库之间的分布式事务。
