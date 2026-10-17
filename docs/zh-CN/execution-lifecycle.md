# 执行生命周期

[English](../execution-lifecycle.md) | [简体中文](execution-lifecycle.md) | [中文首页](../../README.zh-CN.md)

本文介绍本仓库实现中的领取、执行、检查点与重试流程。

1. Submit 原子创建 Run 元数据和版本为零的历史记录。
2. ClaimBatch 使用 FOR UPDATE SKIP LOCKED 按优先级与可用时间选择候选任务，
   检查 lease_epoch < max_attempts，在同一条 SQL 中递增 Epoch 并设置持有者与到期时间。
   不走队列的手动 Claim 也会检查预算。
3. 心跳在较慢的模型或工作区初始化之前启动。续约失败会取消当前 Run。
   领取、续约和受保护的补丁写入共享同一条 Run 记录。
4. 诊断路径先恢复已经持久化的待执行工具，再发起下一次模型调用：
   模型决策先保存，工具输出通过后续检查点保存。
   代码修复路径先核对已有补丁状态，再考虑生成新的修改提案。
5. FencedStore.Save 检查持有者、Epoch、TTL 和预期 Revision，再提交更新的快照。
   CodingHandler 负责写入最终 Agent 检查点。
6. Scheduler 释放已完成的任务。终态 Run 不可再次领取。
7. RetryLater 或 Day15 的暂时性失败策略负责释放并重新调度；永久失败终止执行。
   旧持有者或 CAS 冲突不能结束新持有者的任务。
8. 同一个 Scheduler 定期执行有批量与时间限制的 SweepExhausted。
   已释放、租约为空或已过期且预算耗尽的 Run 会进入失败终态，
   包括被 SIGKILL 后来不及调用 RetryLater 的情况。
   有效租约和成功终态仍受保护；失败转换与事件写入保持事务性和幂等性。

## 三类计数器

| 计数器 | 含义 | 何时消耗 |
| --- | --- | --- |
| lease_epoch 与 max_attempts | 成功取得的执行授权次数与上限 | 成功 Claim |
| retry_count | 显式重新调度次数 | RetryLater 或重试策略 |
| rounds_used 与 max_rounds | 新逻辑补丁意图的轮次与上限 | 首次 Prepare |

MaxAttempts、retry_count 与 rounds_used 不能互相替代。
SIGKILL 可能消耗一次领取授权，但没有发生显式重试。
接管不会重置修复预算；恢复已有补丁也不会额外分配一轮补丁。

维护流程默认约每秒运行一次；发生错误会记录日志，现有 Scheduler 继续执行。
