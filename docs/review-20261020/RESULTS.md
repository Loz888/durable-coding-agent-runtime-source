# 实验结果、改进与证据范围 / Results and evidence scope

<!-- runtime-followup:start -->
## 2026-10-22 续批最终结果

本节优先于下方前批 338 等快照；[续批证据索引](../../reports/runtime-followup-20261022/README.md)记录实际命令、源码指纹与修改清单。仍为本地未提交工作树，没有新远端 CI 或付费调用。

| 检查 | 实际结果 | 说明 |
|---|---|---|
| 全项目 `go test -json -p 1 -count=1 -timeout 360s ./...` | 407 / 0 / 0，162.924s | PASS / FAIL / SKIP；最终代码运行前后不变 |
| 全项目 `go test -json -race -p 1 -count=1 -timeout 360s ./...` | 407 / 0 / 0，225.091s | 代码指纹与完整回归一致 |
| `bash scripts/test_unit.sh` | Go 206 / 0 / 0；Python 7 项通过 | 无数据库 / 模型配置，新增检查已进入现有 CI 入口 |
| `go test -json -tags dockeraccept -p 1 -count=1 -timeout 600s ./executor -run '^TestSealedInputsRealDocker$'` | 5 / 0 / 0，70.316s | 父测试加 4 个真实场景；vendor/workspace 各成功与真实断言失败 |
| `go run ./cmd/sandbox-accept -image <PINNED_IMAGE> -output <NEW_PRIVATE_JSON>` | 程序 4 场景 PASS，44.933s | 默认隔离、超时和运行中 Worker 被杀后的回收，0 残留 |
| `go vet ./...` / `go build ./...` / 格式 / diff | PASS | 仅本地，不替代远端 CI |

上述 Go 数量含父 / 子测试与辅助入口，不是独立故障数。上游缺陷的预期失败对照单列，不混入源码全量回归统计。

**新增实测**：[12 轮共 12003 任务](../../reports/runtime-followup-20261022/benchmark-summary.json)，全部完成，失败 / 额外领取 0；主时长为同进程单调计时，保留 6 个 DB 墙钟负时长。35s 合成回调超过初始 30s TTL 的三轮均完成；池 4 / 20、8 Worker、每轮 1000 任务的吞吐中位数为 142.58 / 157.28 tasks/s，空池成功 Acquire 累计等待中位数为 6162.47 / 0ms（并发等待之和，非单请求延迟；池20首轮仍有等待）。持续流三轮各 2000 任务、50ms 到达间隔，实际单轮 107.03–107.34s，受到达速率限制。均为合成负载，不是模型吞吐、生产容量或长期 soak。

**只读制品盘点**：[实际 CLI 验收](../../reports/runtime-followup-20261022/artifact-inventory.json)使用本轮新建私有目录和测试 schema，5 类均符合预期，文件与引用未改，临时 schema 已清理。先 Put 后登记引用存在并发窗口，两次观察或文件年龄不能授权删除；真正 GC 仍未实现。

**真实上游种子**：[go-humanize issue 130](https://github.com/dustin/go-humanize/issues/130)，固定父版本 `47eb3ae4df4420a7daf4d673dfb5e2149657570c` 与[已知修复](https://github.com/dustin/go-humanize/commit/402bd47cf34e515e11d0dd8d04279a6b4ff04bc2)。[原始哈希、许可与结果](../../reports/runtime-followup-20261022/public-defect-seed.json)：原版完整测试 43/0/0；原版叠加冻结回归 0/1/0（唯一预期断言失败）；已知修复同回归 1/0/0、全量 44/0/0。四次均在固定镜像禁网非 root 容器，未改生产基线或 go.mod；测试 overlay 显式记录。模型调用 0，尚未接入 Runtime，不是模型成绩、代表性数据集或独立人工评审。

**时钟 P0 尚未修复**：[修正采样不确定度后的90秒观测](../../reports/runtime-followup-20261022/clock-observation.json)仍检测系统 / PG 各 3 次倒退，无采样失败。探针退出1表示成功发现 ANOMALY，不是单测失败。WSL 时间服务尚未改动；候选原因因果 A/B 仍待此前单独授权。

首轮失败原样保留：新 Docker 测试目录未显式0700，修正夹具并保留生产权限 guard 后四场景通过；探针初版把 conninfo 放入 PGDATABASE 造成连接失败，改为有界解析至环境；代码复核发现两次读时之间暂停可能误报，补前后单调采样界限与回归后重测。未删除场景、SKIP 或放宽断言。
<!-- runtime-followup:end -->

本轮时钟诊断补充：独立采样25364次系统时钟、265次数据库时钟，观察到8次约2.9秒墙钟倒退；持续流出现7个DB负时长，原始观测与异常均保留。systemd-timesyncd处于active、轮询32秒，chrony未运行。Ubuntu官方说明来宾NTP可能与Windows/Hyper-V同步冲突，与本机现象一致；具体因果仍需获准后做停止服务的A/B对照，当前未修改服务。参考：[Ubuntu时间同步说明](https://ubuntu.com/wsl/docs/stable/explanation/time-sync/)。


## 前批验收快照 · 2026-10-22（续批前）

本节绑定未提交工作树的[源码/配置指纹与机器记录](../../reports/runtime-remaining-20261022/verification.json)。所有最终运行的代码指纹一致。旧301/309及中间336记录不替代本节。[修改文件清单](../../reports/runtime-remaining-20261022/changes.json)按初始未提交快照区分本轮触及，未覆盖原有备份。没有提交、推送或新远端CI。

两个固定专用测试数据库位于自建PG16；离线检查显式移除真实Key。计数含父/子测试及辅助入口，不是独立故障种数。

| 实际命令 | PASS / FAIL / SKIP | 耗时 |
|---|---|---|
| `/usr/local/go/bin/go test -json -p 1 -count=1 -timeout 360s ./...` | 338 / 0 / 0 | 180.444s |
| `/usr/local/go/bin/go test -json -race -p 1 -count=1 -timeout 360s ./...` | 338 / 0 / 0 | 222.731s |
| `/usr/local/go/bin/go test -json -race -p 1 -count=10 -timeout 1200s ./internal/integration20 -run MultiFile|Proposal|Portable|WorktreeRet` | 220 / 0 / 0 | 368.376s |
| `/usr/local/go/bin/go test -json -tags dockeraccept -p 1 -count=1 -timeout 150s ./internal/integration20 -run ^TestPortableIsolatedContainerHosts$` | 1 / 0 / 0 | 29.774s |
| `bash scripts/test_unit.sh` | 153 / 0 / 0 | 6.969s |

vet、build、格式、diff检查均退出0。真实Docker验收另有4个程序场景，不混入Go测试事件。原有Claim、SIGKILL、Fencing、预算、身份、行锁竞争与原仓库干净断言保留。

- [沙箱](../../reports/runtime-remaining-20261022/sandbox.json)：正向编译、隔离边界、超时与运行中Worker被SIGKILL后的精确回收；不同根sentinel保留、容器0残留。不是内核逃逸证明。
- 双容器：固定Go镜像、独立A/B目录、第三个鉴权制品服务；A提交verified_pass/制品后SIGKILL exit137，租约自然到期、B真实Epoch2领取，重建物理身份并重验，恢复模型0、clone干净。只有一台物理电脑。
- [OTLP实际后端](../../reports/runtime-remaining-20261022/observability.json)：读到新Trace的3个Span、Prometheus错误指标及firing受控告警，无外部联系人。短暂503重试/永久不可用是离线故障注入；SIGKILL仍可能丢缓冲。
- [Live冻结任务](../../reports/runtime-remaining-20261022/live-evaluation.json)：12项合成任务、11请求；9新修复+1恢复对照通过未发送给模型的保留断言，1不可修复对照按预期失败，1已通过基线零调用。3382输入/4201输出Token；峰时cache-miss估价$0.0060558，非账单。收到的型号字段是deepseek-flash别名。
- Live先于后续制品/依赖收口，绑定实际执行二进制/源码收据。最终完整/race是离线复验，没有付费重放整个最新版本。原2美元授权没有重置；终态Run退队，费用账本/响应保留。
- [数据库恢复](../../reports/runtime-remaining-20261022/database-restore.json)：在第二个私有本地PG集群恢复新建合成库和只读角色，2行可读、写入拒绝；自建源库/角色已删除，目标PG已停止。未改原5432库。
- sealed-vendor/workspace、错误基线/损坏制品、重复接管、旧制品加新补丁链、映射后的最后Claim收尾、目录dry-run/退役意图协调均有实际回归。

### 负载测量

3轮中位数（组内最小–最大），总吞吐包含入队；持续流包含到达间隔。P95列是每轮P95的中位数，不是合并样本P95。固定等待与真实Go测试分开，使用共享可预热构建缓存，不调用模型，不证明生产容量/SLA。

| 负载 | Worker | 总任务 | 总吞吐中位数（范围）tasks/s | 每轮E2E P95中位数 ms |
|---|---:|---:|---:|---:|
| benchmark-burst | 1 | 600 | 30.74 (30.42–30.98) | 5782.01 |
| benchmark-burst | 2 | 600 | 56.05 (43.36–57.76) | 2990.85 |
| benchmark-burst | 4 | 600 | 98.05 (96.88–100.51) | 1544.14 |
| benchmark-burst | 8 | 600 | 154.21 (146.64–155.10) | 835.49 |
| benchmark-stream | 2 | 4500 | 42.86 (42.85–42.87) | 37.59 |
| benchmark-stream | 4 | 4500 | 42.80 (42.79–42.84) | 37.40 |
| benchmark-go-test | 1 | 36 | 2.89 (2.86–3.04) | 4090.07 |
| benchmark-go-test | 2 | 36 | 6.70 (6.59–6.81) | 1743.93 |
| benchmark-go-test | 4 | 36 | 10.13 (10.12–10.15) | 1135.57 |

[各组机器结果](../../reports/runtime-remaining-20261022/benchmark-summary.json)保留失败、额外Claim、实际Go测试次数、DB异常和清理状态。[独立时钟观测](../../reports/runtime-remaining-20261022/clock-observation.json)不修改主机时间。主要使用Go单调计时；DB异常计数及首个异常三元组保留。历史两条负时长的具体根因未证实，Lease仍依赖数据库墙钟与合理TTL。

### 失败与限制

首次失败日志单独保留：夹具内嵌套Run锁、容器tmp noexec、Unix socket过长、真实评测审计污染活动队列/测试准入配置，以及回放代码误用Plan返回摘要。均未删场景、SKIP或放宽断言；准入测试只临时拥有并恢复自己的配置，真实账本保留。

应用001–016后复现，具体步骤、参数、故障边界和清理见[实现](IMPLEMENTATION.md#2026-10-22-本地升级实现与验收范围)及[演示](../demo-script.md)。原始Key、连接值、日志和数据备份没有复制到仓库。源与showcase只做本地对齐。

仍未完成/未验证：真实第二台物理主机、网络分区/断电；代表性真实开源缺陷和独立人工判定；长期生产压力与外部告警接收人；历史时钟异常根因。多文件/有界离线输入不是任意仓库能力，SQL/文件/制品非跨介质事务，不声明Exactly-once。

三个理解问题和30–60分钟独立练习在实现说明末尾，等待使用者完成，未代替作答。


---

以下保留历史验收记录。


> 版本说明：下面的 2026-10-20 结果和待办保留为历史快照。2026-10-21 的受控验证输入摘要与最后 Claim 收尾以本页末尾更新和本地 engineering-20261021 记录为准；尚未提交或推送。

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

## 2026-10-20 本地交付复验

[机器记录](../../reports/review-20261020/local-walkthrough.json)绑定基线 fdbfacb 加实际受测文件哈希；这是未推送的本地修改，不把基线冒充最终快照。

| 实际命令（已注入隔离测试环境，不展示连接值） | 结果 |
|---|---|
| bash scripts/setup_test_db.sh | 001–009 通过 |
| go test -json -count=1 -timeout 360s ./... | 260 PASS / 0 FAIL / 0 SKIP |
| go test -json -race -count=1 -timeout 360s ./... | 260 / 0 / 0 |
| bash scripts/demo_mock.sh | PASS |
| bash scripts/demo_crash_recovery.sh | PASS：最终检查点窗口 + 四状态矩阵 |
| go vet ./...；bash scripts/check_fmt.sh | PASS |

外层驱动已清除 DEEPSEEK 环境。实际使用新建 PG16 集群、两个固定专用测试库和现有 Go1.27.1；结束后仅停止自建集群。原 5432 集群保持运行。测试计数含父/子测试，不是独立故障种类数。本轮付费调用 0。

演示新增可读阶段日志，未改变协议或断言；完整步骤见[演示](../demo-script.md)。实际观察：Epoch 1 → 子进程 SIGKILL → verified_pass 留存而 Agent 仍 running → 真实 ClaimBatch 得到 Epoch 2 → succeeded / tests_passed / revision 1，逻辑修复轮数保持 1，恢复分支禁止再次调用模型。

D：不可信代码真实容器隔离、多主机、掉电、生产容量/SLA、当前模式的新 Live 成绩未验证。C：代码设计的限制见[能力索引](IMPLEMENTATION.md#2026-10-20-本地交付复验能力与证据索引)。本轮未推送或发布材料。
## 2026-10-21：面试前工程收口的最终本地验收

此节是最终本地工作树，不是新增提交或远端 CI。基线 `fdbfacbb77a4fcf0af118a5f22e8393e7160130e` 加[完整代码/配置指纹和机器记录](../../reports/review-20261020/engineering-20261021.json)标识实际源码；full/race 前后指纹一致。旧 260、292 和 296 均属于此前快照，不能代替此节。

### 修改前与修复后

- 已实际复现 2 个回归失败：目标 `math.go` 不变但测试改变后旧 PASS 被复用；真实 `max_attempts=1` Claim、verified_pass 落盘、最终 Checkpoint 前 SIGKILL 后，普通预算耗尽路径将任务判失败。命令为 `go test -json -count=1 ./durable ./internal/integration20 -run '^TestInterview'`；该修改前日志保留在本机，未改写成成功。
- 修复后覆盖缓存命中、测试/其他源码/模块/策略/工具链改变、捕获副本与当前候选被改、无摘要历史记录、旧 Epoch/身份拒绝；保留四状态真实 SIGKILL 和原始 Repository 干净断言。
- 最后 Claim 的恢复现在是受限证据收尾，不新增 Claim/Epoch、修复轮次、模型或测试；双收尾、原 Sweep、受限身份 HTTP 取消竞争和事务失败回滚有实际 PG 回归。
- 收尾只读登记身份，付费准入在正常 Execute 的包装器中检查。`coding:deepseek` 标签的合成夹具证明预算 0 时正常执行拒绝，但既有证明可免费收尾，额外模型/测试均 0。这是本地协议测试，不是 DeepSeek 调用。
- Worker 将默认相对 `worktrees` 在启动时规范为绝对路径；重启仍需同一配置、有效本地 Worktree 和相同基线，不支持跨主机接管。

### 最终命令与结果

以下命令均通过私有驱动显式移除 `DEEPSEEK_API_KEY` 并注入新建 PG16 的固定专用测试库；没有回退个人库或正式库。`-json` 用于准确计数；计数包含父测试和子测试事件。

| 实际命令 | PASS / FAIL / SKIP | 退出码 |
|---|---|---|
| `env -u DEEPSEEK_API_KEY go test -json -v -count=1 -timeout 360s ./...` | 301 / 0 / 0 | 0 |
| `env -u DEEPSEEK_API_KEY go test -json -race -count=1 -timeout 360s ./...` | 301 / 0 / 0 | 0 |
| `env -u DEEPSEEK_API_KEY go test -json -p 1 -count=10 -timeout 1800s ./repair20 ./durable ./repair19 ./internal/integration20 ./reliability15` | 1360 / 0 / 0 | 0 |
| `env -u DEEPSEEK_API_KEY go test -json -race -count=10 -timeout 600s ./pilotapi -run '^TestInterviewAuthenticatedCancelRacesEvidenceOnlyCompletion$'` | 10 / 0 / 0 | 0 |
| `bash scripts/demo_mock.sh` | 1 / 0 / 0 | 0 |
| `bash scripts/demo_crash_recovery.sh` | 7 / 0 / 0 | 0 |
| `go vet ./...`；`bash scripts/check_fmt.sh`；`git diff --check` | PASS | 0 |

首次五包十轮使用 `-timeout 600s` 的运行退出 1：已出现 1007 个通过事件，但 durable 包触发 10 分钟超时，不能算通过。随后提高到 1800 秒并以 `-p 1` 串行运行包，全部五包十轮完成（804.374 秒）；没有删除或减少场景，包内并发/竞争断言保留。重复验收后的修改仅在 Worker main 和同包测试，选中五个库包未再变化；最终 main 已被上述全量/race 覆盖。

更新后的 SIGKILL 演示同时保留两种路径：预算尚有余量时真实 Claim 令 Epoch 1→2；最后一次 Claim 耗尽时证据收尾保持 Epoch 1。都核对最终 `succeeded/tests_passed`，恢复分支模型调用 0；四状态矩阵仍实际执行。它是同机子进程和真实数据库控制路径，不是完整生产守护进程/掉电验收。步骤及清理范围见[演示文档](../demo-script.md)。Day19 旧合成演示脚本只做兼容/语法检查，本轮未运行该入口。

### 升级、费用和未验证项

先停旧 Worker，应用迁移 001–011，再启用配置好 `FinalizeCoding` 的新 Worker。010 给历史记录空摘要，只有有效执行授权下真实重验才产生新证明；不能按当前内容回填后追认旧 PASS。011 的 pending 信号让普通 Sweep 等待证据检查；它不是成功证明。无正确 profile 的新版 Worker 或数据库/检查暂时错误时需运维处理；工作目录缺失、身份或摘要不匹配会作为无效证明收尾为失败。旧二进制不能混跑；回退应先排空或人工审核这些状态，不直接删列。未在原 5432 数据库应用迁移。

请求代码默认模型 `deepseek-flash`、High、`max_tokens=8192`（repair18 与 repodiag），Worker 从显式模型参数/既有工厂组装请求。本轮只核对代码与协议模拟，无新的真实请求/供应商返回型号；别名不是精确模型快照。internal-pilot 编码包装器使用原持久预留和未知 Usage 保留，300/1200 nanodollar/token 是当前代码常量，不是本次验证过的供应商价格或账单。trusted-local/诊断路径并非全部由这份账本覆盖。历史预算没有重置或挪用。

已运行的测试证明受控条件下的协议行为，不证明任意业务正确、Exactly-once 或生产容量。当前仅可信、标准库独立模块和单文件修复；拒绝链接、workspace/vendor、外部 require/replace。候选副本可防止本轮测试读取发生变化的原文件，但仍是可信工具链/测试的宿主执行，不是恶意代码沙箱，也不存在 SQL/文件系统跨介质原子事务。

无补丁基线已通过但缺持久补丁证明的 Run 不在新收尾范围。逻辑模型响应复用、响应到 Prepare 的窗口、未知账单、任意外部副作用、多主机与掉电恢复均未收口。容器适配器已有代码；无引擎，实际隔离 **BLOCKED**。本轮真实模型、真人试用和当前 High 准确率 **未运行**，付费请求 0。

English: final local full/race pass 301 events each; five selected library packages pass 1360 events over ten runs. Parent/subtest events are not independent scenarios. The initial 600-second timeout remains a failed attempt. Complete controlled-input fingerprints and evidence-only last-claim completion are implemented; live-model quality and actual container security remain unverified. No commit or push was performed.

资源清理已核验：本轮两个自建 Runtime PG 集群均已停止；原 5432 集群仍 online。私有原始证据和测试配置保留本机，不整体发布。

## 2026-10-21 扩展批次1最终本地验收

本节对应前一轮 301 事件之后的最终本地工作树，尚未提交/推送；不继承旧远端 CI。基线 fdbfacb 加[209 份代码/迁移/脚本指纹](../../reports/runtime-extension-20261021/verification.json)标识实际受测内容；以下最终运行前后指纹均未改变。原有 33 份未提交文件已备份并核对备份摘要，没有回退已有工作。

所有 Go 命令经私有驱动移除 DEEPSEEK_API_KEY，使用新建 PG16 专用测试库。计数含父/子测试及辅助入口，不是独立故障场景数。

| 实际验证 | PASS / FAIL / SKIP | 结果 |
|---|---|---|
| `go test -json -count=1 -timeout 360s ./...` | 309 / 0 / 0 | 退出0，94.352秒 |
| `go test -json -race -count=1 -timeout 360s ./...` | 309 / 0 / 0 | 退出0，100.644秒 |
| `go test -json -race -p 1 -count=10 -timeout 600s ./durable ./runtimebench ./repaireval ./observability ./executor -run 'TestTrace\|TestSandbox\|TestJSONTracing\|TestDurableBenchmark\|TestDistribution\|TestOracle\|TestFrozenEvaluation'` | 90 / 0 / 0 | 新增相关链路，十轮116.324秒 |
| `bash scripts/test_unit.sh` | 147 / 0 / 0 | 显式移除数据库配置的已有本地入口 |
| `go vet ./...`；`go build ./...`；`bash scripts/check_fmt.sh`；`git diff --check` | — | 均退出0 |
| `bash scripts/setup_test_db.sh` | — | 001–012重放退出0 |
| `go run ./cmd/repair-eval -trusted-fixtures -output NEW_DIR -trace-file NEW_FILE` | 不按Go测试计数 | 20/20预期行为，退出0 |
| `go run ./cmd/durable-benchmark -tasks 200 -workers 1,2,4,8 -repetitions 3 -delay 20ms -output NEW_FILE` | 不按Go测试计数 | 12轮/2400完成、0失败、0额外领取 |
| `sandbox-accept -output NEW_FILE`（实际构建的最终二进制） | **未执行容器场景** | **BLOCKED，退出2：缺Docker** |

NEW_DIR/NEW_FILE是实际私有证据路径的脱敏写法；报告中以 $EVIDENCE 表示该目录。全量命令及退出码见机器记录。初次数据库墙钟计时的两次基准失败与沙箱目录回归修复前的1个失败保留在本机，不改写为PASS。

### 持久队列基准的最终读数

[完整12轮数据](../../reports/runtime-extension-20261021/throughput.json)。每轮200任务、callback固定20ms、各配置3轮，下表为每组三轮中位数；真实Submit/ClaimBatch/Scheduler/FencedStore，预提交后排空。追踪关闭，Go1.27.1、PG16.15、WSL/Linux amd64、20可见CPU/GOMAXPROCS、池上限20；不是专用隔离硬件，不执行模型/仓库测试。

| Worker槽数 | 排空 tasks/s | 包含预提交的总 tasks/s |
|---:|---:|---:|
| 1 | 21.66 | 18.85 |
| 2 | 65.55 | 57.63 |
| 4 | 69.16 | 46.81 |
| 8 | 138.77 | 69.72 |

主要耗时使用单进程Go单调时钟；不是API P95。最终运行还观察到2条数据库墙钟负时长，首个原值为排队1441.883ms、领取到终态−1225.205ms、创建到终态216.678ms；原值保留，未截零，根因尚未确认。数据库时间仍是Lease前提，不因改用基准单调计时而消除该限制。此前探索轮排空中位数33.21/66.42/128.91/237.96 tasks/s与最终读数有明显差异；不挑最好成绩，不宣称线性扩展、生产QPS或SLA。

### 独立校验与追踪

[冻结评测报告](../../reports/runtime-extension-20261021/repair-evaluation.json)记录14个正确脚本补丁、2个原本通过、2个预算耗尽、1个无效片段、1个故意过拟合。20/20是预期行为符合，包含正确失败；17个可见测试通过，16个独立Oracle通过，1个可见通过/Oracle失败是预设负对照，不能当成模型修复率。全部真实Claim的Epoch为1、原仓库保持干净、自己的行/临时目录完成清理。每个候选和Oracle输入另有摘要；冻结清单SHA为`48c8a693c519aae44b63daf9c3ab26cbb422a2804d5c46f328ecab4e53a7c014`。没有新增真实模型调用。

真实OpenTelemetry SDK本地导出151个评测span，[单条实际链路样本](../../reports/runtime-extension-20261021/trace-sample.jsonl)来自真实领取后直接调用CodingHandler的离线驱动，不是常驻Worker。独立子进程回归另验证Submit保存的W3C上下文在新Worker进程恢复，TraceID/ParentSpanID一致，并提交最终Checkpoint。导出不含Prompt/源码/仓库路径/连接串/原始错误；仍是可选NDJSON，不是OTLP平台，SIGKILL可丢未flush的span。

### 沙箱、升级与未完成项

已实际复现候选根0700导致容器非root UID无法读取的文件系统回归；修复为私有0700宿主父目录、可遍历的只读bind root与0444文件，保持readonly挂载。Worker策略更新为sandbox-go-v2，使旧验证策略缓存失效；有合法执行权时需重验。[真实引擎预检](../../reports/runtime-extension-20261021/sandbox.json)明确BLOCKED，没有运行容器编译/隔离/资源限制/超时场景；不能用文件权限回归代替安全验收。

启用追踪前停旧Worker、应用001–012，再启动同配置新Worker。默认追踪关闭，追踪字段不授予执行权限。旧010/011的升级与降级限制仍适用。

仍未完成：多文件Patch协议、跨主机Worktree重建、Worker SIGKILL孤儿容器回收、真实容器隔离验收、OTLP/Metrics/告警、更广泛真实仓库/真实模型质量评测。本批付费请求0，没有挪用历史预算；未推送/发布。资源检查确认本轮benchmark/eval/trace行剩余0。

English: full/race pass 309 events each; ten targeted race runs pass 90; database-independent entrypoint passes 147. Real-PG synthetic benchmark completes 2400 tasks; final wall-clock anomalies are retained. Frozen scripted evaluation matches 20 expectations, with 17 visible positives, 16 oracle positives and one intentional overfit control. Real container acceptance remains BLOCKED. No paid calls or publication.

扩展批次资源收尾已核验：本轮18569私有PG集群停止，原5432仍online；没有清空其他队列或修改原数据库迁移。当前工作树扫描没有未解决的高置信度密钥/已知个人路径命中，CI本地临时测试凭据已单独归类；扫描不涵盖Git历史或外部缓存。
