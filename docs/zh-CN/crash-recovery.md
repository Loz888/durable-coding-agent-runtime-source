# 崩溃恢复

[English](../crash-recovery.md) | [简体中文](crash-recovery.md) | [中文首页](../../README.zh-CN.md)

本文介绍本仓库实现中的接管与恢复协议。

SIGKILL 会阻止清理、租约释放和最终检查点写入。
此时 PostgreSQL 可能仍记录 running，而文件中已经包含补丁。

替代 Worker 必须在租约过期后通过真实 Claim 获得新的执行授权。
Epoch 只能通过 Claim 推进；即使旧进程随后恢复，它的写入也会被拒绝。
Revision CAS 独立保护检查点版本，避免同一版本被重复覆盖。

## 按持久化状态恢复

repair20.Agent 先打开已经绑定的 Worktree，再按表示因果顺序的 round_number
扫描可恢复补丁，而非依赖 created_at 的墙上时钟顺序。

- prepared：根据当前文件 SHA 判断补丁是否已经写入。
- applied：执行验证。
- verified_pass / verified_fail：复用已持久化的测试证据。
- 只有验证失败且共享预算仍有余量时，才允许生成下一次提案。

verified_pass 补丁可能已经保存，但最终 Agent 检查点尚未完成。
CodingHandler 可以恢复这份结果，不再次调用模型，再通过 FencedStore 完成检查点。
补丁完成与 Run 完成属于不同的持久化边界。

## 已覆盖的故障场景

崩溃恢复测试覆盖四种持久化状态后的子进程 SIGKILL，
以及 verified_pass 已保存、最终检查点尚未提交之间的真实 SIGKILL。

断言覆盖真实 ClaimBatch 接管、Epoch 隔离校验、缓存判定复用、
共享修复预算以及原始源码不被修改。
测试仅推进自身 Run 的到期时间以缩短等待，不直接生成 Epoch。
只终止测试自己创建的子进程，不终止外部 Shell 或数据库。

Mock 测试入口驱动现有 Scheduler.execute；
检查点窗口恢复调用同一公开 CodingHandler 接口。
这些是有明确范围的集成演示，不能作为长期负载基准。

## 恢复边界

Rename 与 Commit 仍是分开的操作。
演示通过不代表未知文件内容、丢失或被替换的 Worktree、
恶意并发写入、断电或跨主机恢复已经得到安全处理。
