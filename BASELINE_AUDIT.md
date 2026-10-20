# 企业内部试点基线审计

审计时间：2026-10-19T03:00:09.548284+00:00。基线 SHA：`ba93bccf6f689885b8833024cbaa2b4d29e90ed9`。分支：`upgrade/internal-pilot-v1`。

没有团队 API、摘要 Token、执行沙箱、跨 Worker 模型准入、费用账本或运维恢复证据。保留 Queue/Scheduler/Lease/Patch Store，禁止旁路重写。

范围：授权团队、合成/公开夹具、单 PostgreSQL、单主机受控持久目录。WSL2 不等于独立 Linux 生产主机。没有适用 AGENTS.md；未修改旧学习目录、公开仓库状态或远程分支。

基线命令、状态、预注册质量/性能/恢复目标见 [baseline.json](reports/pilot/baseline.json)。历史结果不覆盖本轮修改。零安全越权、原回归零 FAIL/SKIP；真实效能目标仅适用于冻结允许范围，未达标限制试点，不改题或评分。性能目标不承诺绝对 QPS。

执行顺序：身份与隔离 → 有界执行与恢复 → 全局预算/取消 → 故障、模型、性能 → 部署恢复与证据。真实模型只使用既有明确授权，不因本地存在 Key 新增预算。运行环境和源 SHA 必须随验收记录更新。
