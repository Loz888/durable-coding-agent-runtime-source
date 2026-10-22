# 3～5 分钟：真实 SIGKILL 与补丁接管（Mock 提案）

<!-- runtime-followup:start -->
## 2026-10-22 续批复现（全部免费、无模型）

在源码目录配置两个专用测试库并应用001–016迁移。Docker用已预加载固定摘要的 `RUNTIME_SANDBOX_IMAGE`，缺配置直接失败，不拉取镜像、不回退宿主测试。新建输出目录只存本次数据：

```bash
OUT="$(mktemp -d "${TMPDIR:-/tmp}/runtime-followup.XXXXXX")"
chmod 700 "$OUT"
env -u DEEPSEEK_API_KEY bash scripts/test_unit.sh
env -u DEEPSEEK_API_KEY go test -v -tags dockeraccept -count=1 -timeout 600s ./executor -run '^TestSealedInputsRealDocker$'
python3 -B scripts/clock_probe.py --postgres --duration 90 --output "$OUT/clock.json"
```

时钟探针0=本窗口未见步进，1=观测到异常，2=配置/采样不完整；它不修改服务。1不是全项目单测失败。不要把90秒无异常推广成永久稳定。

```bash
# ARTIFACT_ROOT必须由操作者明确选择已有私有制品目录；本命令只读，不创建或删除。
env -u DEEPSEEK_API_KEY go run ./cmd/artifact-inventory -root "$ARTIFACT_ROOT" -reference-schemas day19 -database-env DAY12_TEST_DATABASE_URL
# 按以下顺序串行执行；不要并发跑回归/构建以免污染性能读数。
env -u DEEPSEEK_API_KEY go run ./cmd/durable-benchmark -tasks 1 -workers 1 -repetitions 3 -delay 35s -pool-max-conns 4 -output "$OUT/slow.json"
env -u DEEPSEEK_API_KEY go run ./cmd/durable-benchmark -tasks 1000 -workers 8 -repetitions 3 -pool-max-conns 4 -output "$OUT/pool4.json"
env -u DEEPSEEK_API_KEY go run ./cmd/durable-benchmark -tasks 1000 -workers 8 -repetitions 3 -pool-max-conns 20 -output "$OUT/pool20.json"
env -u DEEPSEEK_API_KEY go run ./cmd/durable-benchmark -tasks 2000 -workers 4 -repetitions 3 -arrival-every 50ms -pool-max-conns 4 -output "$OUT/stream.json"
```

观察：35s回调超过初始TTL仍最终提交；每轮失败/额外Claim应为0；池统计是本轮累计差值；持续流吞吐受50ms到达间隔限制；负DB时长原值保留。盘点的currently_unreferenced不代表允许删除，schema列表也不证明包含所有引用者。

清理：基准只删除自己的随机前缀Run；沙箱测试只清理自己的容器/快照；盘点和探针不删任何数据。输出留在本次私有 `$OUT` 供查验。不要用清理脚本删除原仓库、Run/费用审计或他人制品。

真实开源缺陷种子的固定下载URL、archive/LICENSE/回归哈希、四次实际Docker命令及预期失败在[种子记录](../reports/runtime-followup-20261022/public-defect-seed.json)。它不是模型修复；后续接Runtime前还需建立显式受管Git夹具并保留上游SHA。所有本轮实际观察见[续批报告](../reports/runtime-followup-20261022/README.md)。
<!-- runtime-followup:end -->

## 2026-10-22 新增演示：部分多文件写入与可迁移恢复

本轮演示全部使用合成 fixture 和专用数据库；不发消息，不调用模型，不操作原业务仓库。

```bash
# 配好两个专用测试库并执行全部迁移后：
env -u DEEPSEEK_API_KEY go test -v -count=1 -timeout 120s   ./internal/integration20 -run '^TestMultiFileRealSIGKILLRecoveryMatrix$'
env -u DEEPSEEK_API_KEY go test -v -count=1 -timeout 120s   ./internal/integration20 -run '^TestPortableRealClaimAfterSIGKILL$'
# 固定摘要镜像已在本机、Docker可用时：
env -u DEEPSEEK_API_KEY go test -v -tags dockeraccept -count=1 -timeout 150s   ./internal/integration20 -run '^TestPortableIsolatedContainerHosts$'
```

3–5分钟讲解：

1. **0:00–0:40，问题与边界**：任务先在同一 SQL 事务提交 Run 和 Revision0；Worker 后续从数据库领取。没有 channel 作为持久队列，提交成功不等待 Worker 执行完毕。
2. **0:40–1:40，多文件中断**：一个父 Patch Intent 占一个修复轮次，先全量预检，再逐个 guarded Rename。第一个文件改完后 SIGKILL，SQL 仍 prepared；接管按每个文件 before/after 判定补完，第三种内容拒绝。不存在跨文件与 SQL 的原子事务。
3. **1:40–2:50，独立目录恢复**：先提交内容寻址制品，再终止容器A。B没有挂载A目录，等数据库租约自然到期，经真实 ClaimBatch 得到 Epoch2。它使用同 baseline clone、制品及已提交补丁链创建新物理身份，清空旧验证摘要并重验，修复轮数保持1、恢复模型调用0。
4. **2:50–3:40，Fencing与结果**：旧Owner/Epoch写入被同一Run行锁和条件拒绝。最终 Checkpoint revision/status/verdict 一起核对；Clone未修改，任务成功后不再领取。最后 Claim 的收尾只检查已有证据，不新增执行权限。
5. **3:40–4:30，模型与限制**：展示冻结12项Live的分类结果，不把失败对照混成修复成功；说明11请求的Token/估价、响应已提交复用、未知结果不重试。两个容器仍在一台电脑，不宣称跨物理故障域、断电或Exactly-once。

清理：普通测试由t.Cleanup删除其合成行/独立Schema和临时fixture；Docker验收只删除本轮精确命名/标签的A、B、制品容器和私有网络。真实模型实验的终态Run退队但保留费用/响应审计，不用删账本换取测试通过。观测栈只能用其私有state.json定位后停止。不要全局prune或删除整个worktree根。

常见失败：缺专用数据库/迁移会明确失败；缺Docker/镜像摘要时Docker构建标签验收失败而不SKIP；socket挂载只用于受控本地实验，跨真实网络应配置受限PG连接及TLS制品服务；原源仓库脏/提交不符、制品缺失或篡改、目标路径已被占用且身份不符均拒绝。

以下保留原有单文件与最后Claim演示，按各自日期和范围阅读。


本演示复用生产 Queue、CodingHandler、Patch Store、FencedStore 和现有测试夹具；真实子进程被 SIGKILL，提案来自确定性 Mock。它是测试驱动的 Worker 入口接管，不是常驻 daemon 重启或负载测试。[本轮实跑证据](../reports/review-20261020/local-walkthrough.json)。

## 环境与启动

Linux/WSL2、Go1.27.1、Git、Bash、Python3、PostgreSQL16。只接受 agent_runtime_day14_test 与 agent_runtime_test；测试管理员需能管理夹具角色，不能给生产 API 角色提权。

按[README 快速开始](../README.zh-CN.md#快速开始先运行-mock)配置隔离测试库。首次只执行：

```bash
./scripts/create_test_databases.sh
export DAY12_TEST_DATABASE_URL="host=/var/run/postgresql dbname=agent_runtime_day14_test user=$(id -un) sslmode=disable"
export TEST_DATABASE_URL="host=/var/run/postgresql dbname=agent_runtime_test user=$(id -un) sslmode=disable"
./scripts/setup_test_db.sh
./scripts/demo_mock.sh
./scripts/demo_crash_recovery.sh
```

若环境使用 TCP/SCRAM，使用自己的专用测试连接，从私有配置加载，不在截图、命令回显或反馈中粘贴连接值。已有两个合规测试库也须先停止旧 Worker、设置专用测试环境并运行 `setup_test_db.sh` 应用 001–011，再运行两条 demo；不能省略新增迁移。缺变量/错库会先拒绝；不要改校验去连接业务库。

## 观察点与 4 分钟讲述

| 时间 | 操作与说明 | 预期观察 |
|---|---|---|
| 0:00–0:40 | 说明人工构造 Divide 缺陷、原仓库与受管理 Worktree 的区别；运行 Mock demo | 独立测试与源码隔离断言通过 |
| 0:40–1:40 | 运行 crash demo，观察 DEMO phase 日志 | A 的真实领取 Epoch 1；在 BeforeCheckpoint hook 中 SIGKILL 自己 |
| 1:40–2:30 | 解释落盘边界 | Patch 已是 verified_pass；Agent 仍 running、最终 Checkpoint 未提交 |
| 2:30–3:15 | 观察接管 | 仅将本测试 Run 租约设为过期以缩短演示，再真实 ClaimBatch 取得 Epoch 2；不是直接伪造 Epoch |
| 3:15–4:00 | 核查终态和限制 | succeeded、tests_passed、revision 1、rounds_used 1；恢复 Model 被替换为禁止调用的实现 |

本次实测的上述输出见机器记录。crash demo 还运行 prepared/applied/verified_pass/verified_fail 四状态矩阵；该矩阵与 full/race 中相关用例分别核对旧 Owner 拒绝、Worktree 身份、预算、原仓库未改和锁竞争，不把一条日志当作全部证明。

## 恢复读取什么、哪些可能重复

读取 Run 的 Owner/Epoch/状态、Checkpoint revision、repair_runs 的 Worktree 身份与轮数、patch_ops 的状态/Before/After SHA/测试判定。verified_pass 只有完整验证输入摘要、策略和Worktree身份均匹配时才复用缓存，不再生成补丁；prepared 若实际文件已 Rename 则按 SHA 识别。测试执行后但判定未保存可能再次运行；诊断工具有意图但无输出时会重放；模型返回但未持久化的窗口也不能声称 Exactly-once。

FencedStore 与 Patch Store 在权威 Run 行上校验 Owner、Epoch、有效 TTL，Checkpoint 再核对预期 revision；这阻止旧 Worker 提交受保护结果，不能回滚任意外部副作用。最终成功是持久终态、保存测试判定和对应夹具的独立断言共同成立，不是模型一句“修好了”。

## 清理、常见问题与边界

夹具只终止自己创建的子进程，用 t.Cleanup 管理自建 Run、随机 Patch schema、临时源码和 Worktree；证据 JSONL 的路径由脚本打印，供手工保留，脚本不删除他人日志。中途强制终止父测试可能阻止清理：只核对本轮随机资源，不做全局 DROP、pkill 或目录清空。

- 缺配置：先在同一终端设置两个 TEST 变量；export 空变量不会创建连接值。
- 权限不足：使用隔离测试管理员；生产 API 的受限权限不能运行角色夹具。
- 冷 Go 缓存/端口/锁竞争：保留日志定位；不得删断言或改为 SKIP。可按已有测试说明预热编译。
- 运行中没有 DEMO 日志：配套本地修改未推送；旧公开快照仍有同名断言，可用 go test -v -count=1 ./durable -run '^TestDay20RealSIGKILLBeforeCheckpoint$' 查看测试执行。

只支持可信单文件仓库与同一有效本地 Worktree。trusted-local 测试继承宿主权限；实际沙箱、多主机、断电与生产 SLA 未验收。人工缺陷、Mock 提案、故障注入、历史真实模型各自单独标注。

## 2026-10-21 新增：最后一次领取后的受限收尾

先运行setup_test_db应用001–011并升级新Worker。现有crash脚本现在还要求 TestInterviewLastClaimSIGKILLFinalizesWithoutExecution 通过：真实Claim（max_attempts=1）→实际CodingHandler保存verified_pass与摘要/待收尾信号→真实SIGKILL→仅使本Run租约过期→现有Scheduler维护入口复核证据→succeeded/tests_passed/revision1。

观察 `DEMO phase=evidence_only_terminal`：Epoch仍1、max_attempts仍1、repair_rounds仍1、额外模型调用0。这是证据事务收尾，区别于上面的普通Epoch2执行接管；没有第二次执行Claim。旧Owner无法Verify/Save，原Repository保持干净。摘要不匹配、旧证据缺摘要或非成功补丁不会被成功收尾；不会在已耗尽授权后重新跑测试。

当前快照验证只支持标准库独立Go模块；没有外部依赖/replace/workspace/vendor和链接。测试在候选副本执行；真实容器仍未验收。父测试模拟过期只是缩短等待，不伪造领取代次。完整全回归与竞争/回滚结果见RESULTS，本轮没有新增真实模型测验。

## 扩展批次1的免费本地入口

使用全新的私有证据目录，先设置专用测试库环境、应用001–012迁移。下面命令不读取模型Key，不接受正式库；不要覆盖旧报告。

```bash
bash scripts/setup_test_db.sh
env -u DEEPSEEK_API_KEY go run ./cmd/durable-benchmark -tasks 200 -workers 1,2,4,8 -repetitions 3 -delay 20ms -output /path/to/NEW-throughput.json
env -u DEEPSEEK_API_KEY go run ./cmd/repair-eval -trusted-fixtures -output /path/to/NEW-evaluation -trace-file /path/to/NEW-traces.jsonl
```

基准结果同时查看排空与总吞吐、任务/失败/额外领取计数和清理标志。评测要分别看expected_behaviors、visible_test_positive、oracle_positive及visible_positive_oracle_negative；后者包含故意过拟合负对照。Scripted结果不是模型准确率。

常规提交、pilot API与Worker均可增加`-trace-file /path/to/NEW-component.jsonl`，每进程使用不同新文件；按Run及TraceID关联。默认关闭导出，未提供OTLP服务。真实子进程测试：`go test -v -count=1 ./durable -run '^TestTracePersistenceAcrossWorkerProcess$'`。

Docker安装并启用WSL集成后，先准备可信、已加载且以sha256固定的Go镜像，再运行：

```bash
go run ./cmd/sandbox-accept -image 'IMAGE@sha256:REAL_DIGEST' -output /path/to/NEW-sandbox.json
```

上面的IMAGE/REAL_DIGEST是占位符，需要实际镜像身份。命令不会自动拉取、安装或回退宿主。无引擎时明确BLOCKED并非零退出。真实容器阶段尚未实跑，不以本批文件系统回归代替验收。
