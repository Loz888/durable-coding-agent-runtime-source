# 历史真实 DeepSeek 单文件修复样本 / Historical live repair

已完成的历史样本：Divide 错误实现由乘法改为除法，基线测试失败，一轮补丁后测试通过。原记录包含输入 274 / 输出 94 Token。

- [脱敏结果](result.json)、[合成夹具](fixture.json)、[来源与边界](provenance.json)。
- 仅替换结果中的本机 Worktree 绝对路径，原始文件留在 Git 外；来源 SHA-256 可用于本机核验。
- 原记录没有请求配置快照，不能追认为当前 High、Max 或特定模型版本的成绩。
- 单样本不构成真实修复成功率；与 Day21 的 12 次 Mock 恢复评测分开统计。
- 本次发布没有重跑付费调用，也没有把该历史样本算入最新沙箱验收。

One historical live-model repair succeeded after one patch. Its recorded usage is 274 input / 94 output tokens. Exact model snapshot and reasoning effort were not captured. This sanitized artifact is separate from the deterministic Day21 dataset and from current pilot acceptance.
