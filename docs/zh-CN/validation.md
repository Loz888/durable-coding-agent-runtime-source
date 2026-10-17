# 已记录的实现验证结果

[English](../validation.md) | [简体中文](validation.md) | [中文首页](../../README.zh-CN.md)

先前实现验证环境为 Linux/WSL、Go 1.27.1 和 PostgreSQL 16，未使用模型 API Key。
下列结果是功能测试记录，不是吞吐量或延迟基准。

## 测试结果

| 测试组 | 通过的测试条目 | FAIL | SKIP |
| --- | ---: | ---: | ---: |
| 全项目测试 | 207 | 0 | 0 |
| 全项目竞态检测 | 207 | 0 | 0 |
| repair20 重复十次 | 50 | 0 | 0 |
| durable / repair19 / integration20 | 61 | 0 | 0 |

条目数包含父测试、子测试和崩溃子进程辅助入口。
完整测试使用两个专用 PostgreSQL 测试数据库。
Vet 与构建均以退出码零结束；不依赖数据库的测试通过 127 个条目。

可执行 Mock 演示通过 1 个必需场景；
崩溃演示通过 6 个条目，覆盖四种补丁状态和最终检查点之前的崩溃窗口。

## 语句覆盖率

| 包 | 覆盖率 |
| --- | ---: |
| cmd/day8-server | 87.3% |
| distributed | 66.3% |
| durable | 81.0% |
| eval16 | 89.4% |
| executor | 93.1% |
| httpapi | 58.2% |
| internal/integration20 | 无可测语句，仅包含测试 |
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

没有单元测试的命令包显示零覆盖率或没有测试文件。
外部 Demo 的执行不会自动计入覆盖率插桩。
覆盖率测试涉及的包全部通过；这些数值仅描述该次测量。

## 证据、复现与边界

上述数据来自先前实现的实际验证记录，描述当时的测试结果与覆盖率。
新源码仓库使用独立 Git 历史；运行时代码与测试保留了经过验证的实现。

本仓库提供实现、测试、数据库迁移与运行脚本。
复现步骤见[中文 README](../../README.zh-CN.md)；
新仓库的实时 CI 状态见 [Actions](https://github.com/Loz888/durable-coding-agent-runtime-source/actions)。
历史数值不能代替当前提交的 CI 结果。

测试条目包含父测试、子测试与崩溃辅助入口，不代表独立生产工作负载的数量。
覆盖率属于单次记录，不代表吞吐量或可靠性基准。
真实队列领取、四状态补丁恢复和自建子进程 SIGKILL 测试，
不构成断电安全、任意外部副作用恰好一次或跨主机恢复的证明。
