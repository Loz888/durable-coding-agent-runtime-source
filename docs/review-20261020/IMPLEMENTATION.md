# Runtime：系统评审对照与本轮实现

<!-- runtime-followup:start -->
## 2026-10-22 续批实现与边界

- **输入策略**：`internal/goinput/policy.go` 提供默认独立标准库、sealed-vendor、sealed-workspace 的元数据校验及固定命令规划。`repair17` 与 `executor` 共同依赖它，生产层没有新增反向依赖。规划仅执行 Go 的 `mod/work edit -json`，禁用工具链下载、代理和环境配置，不运行候选程序。
- **沙箱接入**：`executor/sandbox_linux.go` 先复制有界输入，再在最终复制树规划命令；vendor 强制 `-mod=vendor`，Workspace 固定 `GOWORK=/src/go.work`，最多8个根内模块，拒绝 replace、外部 Workspace 依赖与未登记模块。Docker 原非 root、禁网、只读、资源和独立回收边界保留。`cmd/day14-worker/main.go` 显式选择策略，verification policy v4 纳入输入模式，旧缓存不能直接作为新策略证明。
- **只读盘点**：`artifactstore/inventory.go` 和 `cmd/artifact-inventory` 只接受已有私有 canonical 根、显式 schema 及测试库变量；读取文件摘要和两次引用快照。无目录创建、DB修改或删除 API，年龄不作资格判断。先上传后发布引用使“当前未引用”不能推导“可删除”；真正 GC 需独立并发协议。
- **测量**：`runtimebench/benchmark.go` 对每轮记录 pgxpool 累计差值，含 guard、执行、自有清理；连接数仅起止快照、不是峰值。成功 Acquire 总耗时包含池开销，空池成功等待另列，取消单列。CLI 可选 pool2..64，0保留原配置；拒绝池1自锁。固定回调≤90s，按完整 worker 批次估算排空≤2m；35s回调对应40s执行超时，保留30s TTL/1s续租。
- **只读诊断**：`scripts/clock_probe.py` 以单调时钟包围系统/PG墙钟读数，计入采样区间 / RTT 不确定度，输出新0600 JSON；不改时间或服务，不输出DSN，配置失败返回2、发现异常返回1。`scripts/test_unit.sh` 已纳入共享规划器、CLI和7项Python测试。

[实际结果](RESULTS.md) · [续批证据](../../reports/runtime-followup-20261022/README.md) · [复现命令](../demo-script.md)。前批 Live 保留其旧二进制/请求收据，本轮无新模型调用；真实上游种子只有已知补丁对照，未证明 Runtime 或模型能够修复该任务。

需要亲自回答的3个问题：
1. 为什么测试命令要从最终复制树规划，输入模式为什么必须进入验证身份？
2. 两次引用快照都未引用，为什么制品仍可能不可删除？
3. 累计空池等待与单个任务P95分别是什么分母，为什么不能直接互换？

30–60分钟独立练习：为只读盘点补一个“扫描中取消”的案例，验证及时结束且制品与数据库引用不变。先自行实现，不提供答案。
<!-- runtime-followup:end -->

## 2026-10-22 本地升级：实现与验收范围

本节是当前本地工作树；此前各日期的表格、BLOCKED 和“尚未实现”是历史快照。未提交或推送，本页不继承旧远端 CI。

| 方向 | 本轮交付 | 证据与边界 |
|---|---|---|
| 多文件补丁 | 同一逻辑轮次最多 8 个白名单 Go 文件，逐文件 before/after 摘要、全清单预检、统一验证 | 真实 Claim + 5 个 SIGKILL 窗口，含第一个 Rename 后的混合状态；多个 Rename 与 SQL 仍非原子操作 |
| 真实沙箱 | 固定镜像摘要、非 root、只读根、无网络、资源上限、超时；独立按身份清理孤儿 | 已运行真实 Docker；不是恶意内核逃逸证明，docker 组具有管理员级权限 |
| 模型恢复 | 请求意图、已观察响应与 Usage 持久化，恢复先复用响应；未知结果拒绝自动重发 | 3 个实际 SIGKILL 窗口；显式再授权最多 3 个逻辑尝试，物理费用预算另行约束，不保证 provider Exactly-once |
| 真实评测 | 冻结 12 项人工合成任务，High、deepseek-flash、单轮 | 11 次真实调用，3382 输入 / 4201 输出 Token；9 个新修复、1 个恢复对照通过独立断言，1 个不可修复对照按预期失败、1 个基线通过且零调用 |
| OTel | OTLP HTTP → Collector → Jaeger / Prometheus，耗时与错误指标、有界导出重试 | 实际读取 1 条新 Trace / 3 个 Span、指标和 firing 告警；受控错误，无外部联系人，SIGKILL 仍可能丢失缓冲数据 |
| 基准 | 真实 PG 固定等待、持续到达，以及真实 Worktree + Go test 负载 | 使用单调计时，保留数据库墙钟原值与异常计数；具体轮次见机器结果，不代表 LLM 吞吐或生产容量 |
| 可迁移 Worktree | 基线 commit + 有界全文件制品 + 持久化补丁链；新 Claim 下重建身份并重验 | 两个独立容器目录和第三个制品服务已验证；A SIGKILL、自然到期、B 真实 Epoch2 接管；只有一台物理电脑 |
| 输入与生命周期 | 显式 sealed-vendor / sealed-workspace 离线输入策略；终态 Worktree dry-run/受保护退役；数据库与角色恢复实验 | sealed 输入已完成4个真实Docker场景；Workspace限根内模块，不支持任意依赖图；数据库恢复只使用新建合成库 |

真实调用收到的型号字段为 `deepseek-flash`，这是服务端别名，不是不可变模型权重快照。2026-10-22 官方文档将它路由至 DeepSeek-V4.1-Flash-1017。峰时 cache-miss 上界估价 **$0.0060558**，不是供应商账单；本轮授权上限 $2，历史预算没有挪用。原始响应不公开推理内容或 Key。

仍未验证：第二台物理主机、网络分区/断电、任意不可信大型仓库、真实开源缺陷总体修复率、独立人工判定、长时生产压力/SLA，以及历史两条数据库负时长的根因。单调计时没有消除 Lease 对数据库墙钟的依赖。没有自动提交、推送或对外发布。

### 代码入口与持久化边界

- `repair19/batch.go`：PrepareBatch 先检查全部成员，单个父 Intent / 单个修复轮次；Apply 在相同 Run 行锁下逐个 guarded Rename，恢复允许已知 before/after 混合状态，第三种内容拒绝。
- `repair19/proposal.go`、`modelbudget/proposer.go`：先记录请求意图，再调用预算包装器；响应与 Usage 在数据库提交后才可复用。已计费但未观察响应的窗口保持未知，不自动重发。显式 AuthorizeProposalRetry 不清除历史，也不重置费用预算。
- `repair19/portable.go`、`repair17/portable.go`、`artifactstore/store.go`：制品排除 .git、旧主机绝对绑定元数据及受保护文件名；不能自动识别源码内硬编码的秘密，操作员仍需审阅并使用私有存储。仅相同 baseline、相同跟踪文件成员（最多256文件/4MiB原始内容）。目标主机需预置同 commit 干净 clone。缺失/损坏制品、身份/基线不符均拒绝。
- RestorePublished 仅在已有已提交制品时，在新 Claim 下重建物理身份；首次制品提交前的丢盘不在本版自动恢复范围。制品落后于数据库时只回放已提交补丁链。绑定版本单调增加，旧验证摘要清空，新环境必须重验；修复轮数和模型账本不变。第二次接管在重新发布前崩溃也可使用旧制品加已知补丁链恢复。
- `durable/coding.go`、`durable/completion.go`、`repair20/postgres.go`：复用原 Scheduler/Lease/Store。显式 repo-map 将提交时逻辑仓库映射到本机 clone，正常执行与最后 Claim 证据收尾使用同一映射；不复制旧绝对路径来冒充身份。
- `executor/reaper_linux.go`、`cmd/sandbox-reap`：不可变标签包含私有根摘要/候选名/到期时间。只清理精确匹配且过期的容器，候选绑定路径也必须匹配；不同根 sentinel 保留。Worker 的启动/周期清理不能覆盖其死亡期间，独立 supervisor 才能覆盖。
- `repair17/sealed.go`：默认独立标准库策略不扩大；sealed-vendor 强制本地 vendor、禁 replace；sealed-workspace 只允许根内最多8个模块，禁止外部依赖/replace/未登记模块。所有依赖/测试/模块内容参与摘要；续批已新增显式沙箱执行，宿主元数据规划不运行候选测试。
- `repair19/retention.go`：仅单个精确 Run、终态超过保留期且租约到期、受管理目录/身份及唯一绑定符合时可删除。先持久化删除意图；文件删除后 SQL 未提交可重入协调。保留 Run、响应、模型预算及制品；不提供无依据的 blob 自动 GC。
- `observability/otlp.go`：必须显式 loopback endpoint，不继承环境中的授权头，导出错误隐藏原始正文，短暂503有界重试，永久故障返回明确错误。指标只按 operation 聚合，不按 Run ID/DSN 产生标签。

### 升级与使用

先停旧 Worker，备份需要保留的数据，再应用 `scripts/setup_test_db.sh` 中所有 **001–016** 迁移；本轮仅在专用测试库应用。不要混跑旧二进制，回退前排空或人工核对新状态，不直接删列。

```bash
# 需预先配置两个专用测试库；此入口不调用真实模型。
export RUNTIME_SANDBOX_IMAGE='golang@sha256:<verified-local-digest>'
bash scripts/runtime_remaining_accept.sh /tmp/runtime-accept-new

# 单独重放关键矩阵（移除模型 Key）
env -u DEEPSEEK_API_KEY go test -race -count=1 ./internal/integration20
# 仅预加载真实镜像与 Docker 可用时运行；否则明确报错，不 SKIP。
env -u DEEPSEEK_API_KEY go test -tags dockeraccept -count=1 -timeout 150s   ./internal/integration20 -run '^TestPortableIsolatedContainerHosts$'
```

- Worker：`-files a.go,b.go`；可选 `-model-response-cache`；使用 `-artifact-dir /private/artifacts` 或 `-artifact-url https://private-host` 二选一；远端 Token 只从 `ARTIFACT_TOKEN` 环境读取，不写参数或仓库。纯隔离实验可显式 `-artifact-isolated-http`，正常远端应提供 TLS。
- 不同主机使用私有 `-repo-map /private/repo-map.json`：提交的逻辑仓库键 → 本机干净 clone 的绝对路径。映射由操作员提供，不接受模型任意选路径。基线 clone、制品和数据库缺一不可。
- 沙箱独立清理：`go run ./cmd/sandbox-reap -image "$RUNTIME_SANDBOX_IMAGE" -root /private/sandbox-root -interval 15s`。先查看该命令帮助；不要扫描/删除整个 Docker 账户资源。
- 目录退役：`go run ./cmd/worktree-retire -run-id <exact-id> -base /private/worktrees -retention 24h`，默认 dry-run；审阅结果后才加 `-apply`。只影响该任务创建的 Worktree。
- 观测：`python3 scripts/observability_local.py start --dir /private/new-observation`，需本地已有固定摘要镜像；把返回 collector loopback URL 显式给 Worker 的 `-otlp-endpoint`。停止时使用同一脚本 `stop --dir`，不运行全局 prune。
- 数据库恢复：`python3 scripts/postgres_restore_accept.py --source-socket /private/owned-pg-socket --source-port <port> --output /private/new-restore`。创建独立合成源库和第二个私有集群，只验证本实验数据与角色，不读取正式任务库。
- 真实模型入口 `cmd/live-repair-eval` 单独要求冻结 SHA、显式授权预算、新输出目录及本地 Key。已有调用记录时拒绝自动重跑；不要将重新建库当作重置本轮授权。恢复读取既有响应不会重新调用供应商。

### 本轮真实失败与修正

保留首次失败日志：多文件/响应协议、OTLP拒绝与重试、制品锁内重复申请 Run 锁、Docker测试目录 noexec、Unix socket 超长、评测审计污染活动队列统计/测试准入配置，以及新增回放代码的类型/契约错误。之后的通过记录单独存储，不覆盖失败。Go Plan 返回的是 after 摘要；恢复时先校验该摘要，再生成实际替换字节。所有新窗口均保留断言，不使用 SKIP。

### 需要使用者亲自完成

1. 为什么“响应已计费”与“响应已可恢复”是两个边界？请求已发出但响应未提交时为何不能自动重试？
2. 制品只包含较早的 prepared 内容，而 SQL 已是 verified_pass，新主机如何重建？为什么必须清空旧验证摘要再测试？
3. 旧 Worker 即使仍能运行进程，哪一行数据库权限条件使它不能提交？新物理目录身份与逻辑 Run 的预算为何分开？

30–60分钟独立练习：在新的测试文件中为“制品服务返回200但正文被篡改”增加一个失败场景，证明无新绑定、无修复预算消耗、无模型重发，原 clone 保持干净。先自行定位接口和断言，暂不提供答案。


---

以下保留此前版本的路线与验收记录，按日期阅读。


> 版本说明：下面的 2026-10-20 结果和待办保留为历史快照。2026-10-21 的受控验证输入摘要与最后 Claim 收尾以本页末尾更新和本地 engineering-20261021 记录为准；尚未提交或推送。

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

## 实验结果与远端验证

本轮与历史结果的分层、改进映射、源码身份和远端运行见 [RESULTS](RESULTS.md)及[逐次 CI 记录](../../reports/review-20261020/remote-ci.json)。历史原始报告不改写，远端通过只归属于记录中的确切 head SHA。

## 当前验收

[机器记录](../../reports/review-20261020/verification.json)：普通全回归 **260 PASS / 0 FAIL / 0 SKIP**，race 同为 **260/0/0**；独立测试 138 PASS，格式、vet 和 build 通过。计数含 Go 父测试/子测试事件。全部在本轮隔离 PG16 专用测试库运行，不修改原数据库，不调用真实模型。

最终代码对应[测试前源码哈希](source-manifest.json)。Mock/崩溃演示与 Day21 Capture 在前一验证快照通过，随后 645c88e 的远端 CI 已通过这些固定入口，见逐次 CI 记录。历史真实单样本与 Day21 记录均保留，本轮不产生新的真实模型质量结论。

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

## 2026-10-20 本地交付复验：能力与证据索引

标记：A＝本轮阅读关键实现；B＝本轮执行所列自动验证；C＝根据代码推断；D＝尚未验证。B 只说明受测条件通过，不代表生产能力或人工逐时刻观察全部时序。以下表述是项目事实，不能直接代替个人独立贡献。

| 能力与代码位置 | 实际行为 | 验证方法 / 现有证据 | 剩余限制 | 可用项目表述（贡献另核验） |
|---|---|---|---|---|
| A/B Agent Loop：[runner.go](../../durable/runner.go)、[types.go](../../durable/types.go) | 先恢复未完成工具调用，再请求模型；Run 状态为 running/succeeded/failed，排队是 running + queue_enabled，不额外假设 pending 状态 | TestDay12ResumeAfterCommittedToolIntent / AfterSavedToolResult、CompletedRunDoesNotRestart；本轮 full/race | 输出未提交时工具可重放；模型返回与保存间也有窗口 | 以持久化 History 驱动有界 Agent Loop，恢复已提交的工具意图与结果 |
| A/B 队列与背压：[queue.go](../../durable/queue.go)、[scheduler.go](../../durable/scheduler.go) | 按优先级、可用时间 SKIP LOCKED 原子领取；仅按空闲 Worker 槽领取 | TestPre20QueueOrderingAndRetrySemantics、ClaimBudgetAndConcurrency | 槽数是进程内上限；CLI 队列没有统一全局容量承诺 | 通过 PostgreSQL 原子领取和有界执行槽协调并发 Worker |
| A/B Lease/Epoch/Fencing：[lease.go](../../durable/lease.go)、[fenced_store.go](../../durable/fenced_store.go) | 领取递增 Epoch；续约和保存检查持有者、Epoch、数据库时钟 TTL；Revision CAS 排序快照 | TestDay13LeaseClaimRenewFencing、StaleReleaseCannotAffectNewOwner；SIGKILL 矩阵 | 拒绝旧写入不等于能撤销已发生的外部副作用；数据库时钟仍是前提 | 用 Lease/Epoch 与 Revision CAS 拒绝过期 Worker 提交 |
| A/B Checkpoint：[postgres.go](../../durable/postgres.go)、FencedStore | 元数据与 History 检查点同事务保存；冲突/插入失败回滚 | TestDay12PostgresTransactionRollback、TestReviewTerminalCheckpointInsertFailureRollsBackTimeAndVerdict | 没有 SQL 与文件系统跨介质原子提交 | 将运行状态、版本和检查点纳入受保护事务 |
| A/B 补丁恢复：[repair20/runner.go](../../repair20/runner.go)、[repair19/store.go](../../repair19/store.go) | 恢复 prepared/applied/verified_pass/verified_fail；对比 Before/After SHA；复用已保存判定 | TestPre20SIGKILLRecoveryMatrix、TestDay20RealSIGKILLBeforeCheckpoint；[演示](../demo-script.md) | 受控单文件、同一有效本地 Worktree；不是掉电/多主机证明 | 覆盖四种补丁持久状态的真实子进程 SIGKILL 接管 |
| A/B 工具重放：Runner.pendingCalls、[repodiag/tools.go](../../repodiag/tools.go) | 以 call_id 对齐意图与输出；已保存输出不重执行，缺输出则重新执行 | 两个 Day12 Resume 测试；本轮全回归 | 未实现通用外部写操作去重；不声称 Exactly-once | 明确工具意图与结果的提交边界及可能重放条件 |
| A/B 终止与预算：[scheduler.go](../../durable/scheduler.go)、[reliability15](../../reliability15)、[repair20](../../repair20) | 独立的领取授权、显式重试、补丁轮次、Runner turns/calls 与超时；Sweep 耗尽任务 | TestDay15CrashLoopExhaustion、TestPre20ThreeSIGKILLsCannotBypassClaimBudget、TwoRoundBudgetShared | 最后 Claim 后已验证补丁的专门收尾仍是 R2 缺口 | 分离授权、重试与逻辑修复预算，避免接管重置限制 |
| A/B 模型预算：[modelbudget/proposer.go](../../modelbudget/proposer.go)、[Worker](../../cmd/day14-worker/main.go) | internal-pilot 的 coding:deepseek 包装器先持久预留，每尝试计数，未知 Usage 保留；默认付费关闭 | modelbudget 全包与 Provider 故障模拟；本轮未付费 | 不是所有 legacy/trusted-local 模型路径都自动接此账本；没有逻辑响应 Exactly-once | 为试点编码请求实现持久费用预留与未知结果保守记账 |
| A/B 修复与诊断校验：[repair19 Verify](../../repair19/store.go)、[diagnosis.go](../../repodiag/diagnosis.go) | 测试退出状态、引用与摘要检查；执行 succeeded 与证据判定分开 | ReviewDiagnosticCompletionIsNotVerifiedSuccess、StructuredVerdictPersists、UTF-8 等回归 | 测试通过不等于业务正确；完整验证输入摘要 R4 待补 | 区分执行完成、测试结果与诊断证据一致性 |
| A/B/C 工作目录：[repair17](../../repair17)、[repair19](../../repair19)、[sandbox](../../executor/sandbox_linux.go) | Worktree 基线/身份/路径校验；容器适配器在无引擎时拒绝，不回退宿主 | WorktreeIdentityAndSymlinkRejection、RebindingReplacement；沙箱参数/快照负例 | D：实际容器隔离无引擎；trusted-local 测试继承 Worker 权限 | 用受管理 Worktree 隔离受控单文件修改，显式保留执行安全边界 |
| A/B 日志、时间与费用：[QueueSummary](../../reliability15/postgres.go)、[observability](../../observability) | 冻结 finished_at；分开完成/运行/历史未知时间；费用按记录 Usage/预留解释 | ReviewTerminalTimeFreezes、ReviewQueueDurationStops；本轮演示日志 | first_claimed_at 不含首次排队、含后续等待，不是 CPU；Mock Token 未知 | 校正终态耗时口径并对未知 Usage 与历史缺失值显式建模 |

准确命令、执行结果与未验证项见 [RESULTS](RESULTS.md#2026-10-20-本地交付复验)，本轮原始日志只留本机。架构与贡献表述需要讲述者通过独立练习核验后使用。

## 2026-10-21：验证输入与耗尽后收尾

- `repair17/verification.go`：有界候选全树副本、确定性相对路径/文件权限/内容、Worktree身份和基线、Go版本/二进制摘要、白名单构建环境、命令和超时共同形成verification_input_digest。固定GOENV=off/CGO_ENABLED=0/GOWORK=off；当前仅标准库独立模块，不支持外部依赖/replace、workspace/vendor或链接。
- Patch Store的默认验证执行于捕获副本。无补丁的Agent基线测试不缓存，不产生待收尾标记；本次最后Claim收尾范围仍是已有verified_pass补丁。复制前后校验，测试后检查副本和当前候选；变动不发布PASS。其他可信自定义Verifier必须提供稳定策略身份，容器策略包含固定镜像/限制。可信工具链安装、无外部状态的受控测试仍是前提；这不是任意恶意测试的宿主沙箱，也没有SQL/文件系统跨介质原子性。
- 迁移010只增加空摘要；旧verified记录缺摘要不得缓存命中，只有有效执行Lease下真实重验才能获得新证明。
- 验证成功在同事务设置completion_pending（迁移011）。普通Sweep跳过该信号；Scheduler先调用CodingHandler.FinalizeExhausted做证据检查。信号本身不构成成功证明。
- 仅running、排队、同profile、预算耗尽、旧Lease失效的候选可进入收尾。最新补丁必须verified_pass、exit=0、轮数和身份一致、完整摘要匹配；prepared/applied/verified_fail/缺摘要/改测试/身份错误均不得成功。
- 收尾复用同Run行锁和FencedStore保存逻辑；临时唯一Owner只在该事务内授权并消耗，终态提交后清除，无额外Claim、Epoch、修复或模型次数。插入Checkpoint失败则全部回滚。取消、双收尾与Sweep由同Run锁协调；不会复活已取消或终态任务。
- 升级前先停旧 Worker，应用 001–011，再启动配好 FinalizeCoding 的新 Worker；旧二进制不能参与混合升级。无相应 profile 的新版 Worker，或数据库/检查的暂时错误，可能让待检查信号继续等待；需要运维处理而非重置预算。工作目录缺失、身份或摘要不匹配属于无效证明，受限收尾将其写为失败，不提升成功。

本轮收口此前R2，以及R4在上述受控输入范围内的实现；不是任意依赖仓库的全环境证明。R1逻辑模型响应复用、未知结果窗口和R5实际容器验收继续留在路线。

Worker 兼容补充：启动时将默认相对 Worktree base 规范为绝对路径；重启应显式使用同一绝对 `-worktree-base` / `WORKTREE_BASE`，保持受管理目录、工具链和白名单配置。切换启动目录不会自动找到旧目录。internal-pilot 的登记身份检查与付费执行准入分开：预算 0 拒绝新执行，但有效持久证明仍可免费收尾；合成 DeepSeek 标签回归未调用供应商。

### 本轮核对的模型与费用边界

| 路径 | 已读请求配置 | 预算覆盖与限制 |
|---|---|---|
| repair18 编码 | 默认别名 `deepseek-flash`，Thinking enabled，High，输出上限8192；显式 Model 可覆盖ID | internal-pilot 的 coding:deepseek 经既有持久预留；未知 Usage 保留，不是供应商账单 |
| repodiag 诊断 | `deepseek:<modelID>` 经工厂传入，空ID默认 deepseek-flash；High/8192 | 诊断 Usage 覆盖不足；trusted-local 和旧诊断不自动接入同一试点账本 |

本轮没有发送真实请求或观察返回精确型号。供应商别名不等于固定模型快照；代码中的费率/模型常量未在本轮向供应商核价。已知 Usage 的估算、保守预留和实际账单分别解释；历史预算未重置。模型响应至 Prepare 的窗口、逻辑响应复用和未知费用仍是限制，不声称 Exactly-once。最终结果见 RESULTS 的 2026-10-21 节。

## 2026-10-21 扩展批次1：观测、持久队列基准与离线评测

此节是前一轮301事件快照之后的本地扩展，不覆盖历史报告；本轮最终验证见RESULTS与`reports/runtime-extension-20261021/verification.json`。仍未提交或推送。

| 能力 | 实现入口 | 边界 |
|---|---|---|
| 真正持久队列基准 | [runtimebench](../../runtimebench/benchmark.go)、[命令](../../cmd/durable-benchmark/main.go) | 复用Queue/Scheduler/FencedStore；固定延时合成callback，不执行模型或Go修复；只接受专用队列测试库 |
| OpenTelemetry | [SDK/本地导出](../../observability/tracing.go)、[跨进程回归](../../durable/tracing_test.go) | 真实SDK v1.47.0、W3C Trace Context、可选本地NDJSON；尚未接OTLP后端或完整Metrics/告警 |
| 跨领取上下文 | [Queue](../../durable/queue.go)、迁移[012](../../migrations/012_trace_context.sql) | trace_parent/trace_state与初始Run同事务保存，Worker提取；追踪信息不授予执行权、不替代Lease/Epoch |
| 扩大受控评测 | [执行器](../../repaireval/evaluate.go)、[冻结20场景](../../evaldata/repair-extension-20261021/cases.json)、[命令](../../cmd/repair-eval/main.go) | 真实Claim、Patch、Checkpoint和独立Oracle；Scripted数据，不是新Live或真实仓库质量测验 |
| 沙箱准备 | [适配器](../../executor/sandbox_linux.go)、[实际引擎验收入口](../../cmd/sandbox-accept/main.go) | 文件系统回归修复非root UID读取候选根目录的问题；真实隔离仍需Docker。缺引擎返回BLOCKED，不作SKIP/PASS |

### 吞吐量口径

一次试验先用真实Submit预提交固定任务，再启动真实Scheduler排空。分别记录提交耗时、排空吞吐与包含预提交的总吞吐，不能混用。主要时延使用同一基准进程的Go单调时钟：提交开始→callback派发、派发→最终Checkpoint提交返回、提交开始→提交返回；它们不是API请求耗时，也不包含LLM或代码测试。

数据库created_at/first_claimed_at/finished_at的原始差值另行统计。早期两次试跑观察到负差值并中止，原始失败保留；未确认根因，不通过截零掩盖。单调计时版本保留DB异常计数/首个原值，避免把墙钟差值直接用于主要延迟统计。吞吐仅适用于所列硬件、Worker数、池配置、固定负载和本机单Scheduler，不推断生产QPS或多主机容量。

基准持有现有测试队列协调锁；开始时若专用库有未完成排队任务就拒绝，绝不清空他人任务。清理仅删除本次随机Run集合。不得与未遵守实验协调锁的其他生产者共享该实验库。

### 追踪与隐私

CLI提交、Worker及pilot API分别用`-trace-file /path/to/NEW.jsonl`启用，本地文件0600且禁止覆盖。启用前应用迁移001–012。记录服务名、操作名、Run、Epoch、Revision及验证结果；不记录Prompt、源码、仓库路径、连接串、工具输出或原始错误文本。默认未配置时为no-op。

异步链路以提交事务中保存的W3C上下文关联；真实子进程回归检查另一Worker进程的TraceID和ParentSpanID。它证明跨进程传播，**不代表跨主机Worktree恢复**。批量导出在正常退出时flush；SIGKILL可能丢失未导出的尾部span，追踪文件不是持久执行日志或恢复权威。

### 离线评测口径

20个手工指定的可信合成单文件场景：14个正确脚本补丁、2个原本通过、2个预算耗尽、1个无效片段、1个故意过拟合。先冻结输入与独立Oracle，再实现执行器；Oracle不放入模型请求/候选可见测试，而在复制候选上独立执行。原始仓库干净、真实Claim、单轮预算均保留。

“20/20预期行为符合”含正确失败和负对照，不能叫20/20修复成功。过拟合对照应暴露可见测试通过而Oracle失败；原Runtime的tests_passed仍只代表它实际运行的测试。报告分别给出这些分母，不改写历史Day21/Live记录，不使用已有付费授权。

### 沙箱与后续边界

候选父目录必须私有0700，bind root可被容器UID65532读取，文件仍只读，Docker挂载仍readonly。更新的sandbox-go-v2策略身份使旧策略验证缓存失效；需要有效执行授权重验。文件系统断言和参数测试不是容器隔离证明。

sandbox-accept仅对本轮root哈希标签检查容器清理，不删除其他容器。预备检查覆盖真实编译、网络/只读/UID/能力/资源约束和执行超时（包括编译）；Worker SIGKILL后的孤儿回收、完整恶意代码安全评审仍未完成。当前无Docker，真实容器结果为BLOCKED。多文件补丁和跨主机工作目录重建尚未实施，本批不宣称完成。


模型路由与价格参考：[DeepSeek官方价格](https://api-docs.deepseek.com/quick_start/pricing/)、[2026-10-17更新说明](https://api-docs.deepseek.com/updates/)。本地实际返回字段是别名，不能当作精确权重版本。


本轮时钟诊断补充：独立采样25364次系统时钟、265次数据库时钟，观察到8次约2.9秒墙钟倒退；持续流出现7个DB负时长，原始观测与异常均保留。systemd-timesyncd处于active、轮询32秒，chrony未运行。Ubuntu官方说明来宾NTP可能与Windows/Hyper-V同步冲突，与本机现象一致；具体因果仍需获准后做停止服务的A/B对照，当前未修改服务。参考：[Ubuntu时间同步说明](https://ubuntu.com/wsl/docs/stable/explanation/time-sync/)。
