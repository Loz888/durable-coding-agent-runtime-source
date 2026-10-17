# 发布验证记录

[English](../validation.md) | [简体中文](validation.md) | [中文首页](../../README.zh-CN.md)

2026-10-17 在独立发布目录执行验收，环境为 Linux/WSL、Go 1.27.1、
PostgreSQL 16。没有提供真实模型 API Key，只使用两个固定专用测试数据库。

| 验证项 | 通过条目 | FAIL | SKIP |
| --- | ---: | ---: | ---: |
| 全项目普通测试 | 226 | 0 | 0 |
| 全项目竞态测试 | 226 | 0 | 0 |
| 全项目覆盖率测试 | 226 | 0 | 0 |
| repair20 / durable / repair19 十轮回归 | 690 | 0 | 0 |
| 不依赖数据库的测试 | 133 | 0 | 0 |
| Mock Demo | 1 | 0 | 0 |
| 四状态矩阵与 Day20 崩溃 Demo | 6 | 0 | 0 |

条目数包含父测试、子测试与崩溃辅助入口。
格式、构建与 go vet 均通过。验收拒绝测试用例 SKIP；
没有测试文件的包不混算为被跳过的测试。

## Day21 复核

新采集的 12 个确定性试验严格评分全部通过。
已提交的 summary 与 Markdown 报告也逐字节重算一致。
原始场景、观测记录与 400/1403 ms 的历史 P50/P95 保持不变；
新采集延迟单独记录，不将它替换为通用基准。

新脚本输出到独立外部目录，不删除或覆盖已提交 JSONL。
场景、分母、计时边界与样本限制见[评测方法](evaluation.md)。

## 语句覆盖率

| 包 | 覆盖率 |
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

internal/integration20 只有测试，没有生产语句。
无单元测试的命令包显示零覆盖率或没有测试文件。
这些数值只描述本次记录，不代表性能或生产可靠性。

## 远程 CI 与边界

实际远程结果以 [Actions](https://github.com/Loz888/durable-coding-agent-runtime-source/actions) 为准。
CI 通过 TCP 连接 PostgreSQL 16，准备两个专用测试库，应用 001–006 迁移，
执行全量普通与竞态测试、真实 Mock/崩溃 Demo 和 Day21 采集与报告重算。
本地 WSL 使用对应 Unix Socket 连接，不能用本地通过冒充远程成功。

本仓库已公开，未选择软件 License。上表本地结果来自独立发布目录；
相同实现与测试文件通过文件同步进入公开仓库，没有导入私有 Git 历史。
原始开发工程保持只读；本地报告、原始日志、凭据和备份均保留在 Git 之外。
上方 Actions 链接展示本公开仓库自己的 CI 结果。

Day19 Rename、Day20 最终检查点前 SIGKILL、Day21 已验证结果接管属于不同实验；
不宣称沙箱、任意外部副作用恰好一次或跨主机 Worktree 恢复。
