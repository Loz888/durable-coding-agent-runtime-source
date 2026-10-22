# 制品只读盘点

这是本地专用测试数据库的只读观察工具，不是垃圾回收器。它不会创建制品目录、修改数据库或删除文件，也没有 `--apply` 模式。

先在当前终端配置 `DAY12_TEST_DATABASE_URL` 或 `TEST_DATABASE_URL`，不要将连接串写入命令或报告。显式指定已经存在的私有 canonical 制品根目录，以及所有已知引用 schema：

```bash
env -u DEEPSEEK_API_KEY go run ./cmd/artifact-inventory \
  -root "$ARTIFACT_ROOT" \
  -reference-schemas day19 \
  -database-env DAY12_TEST_DATABASE_URL
```

多个 schema 使用逗号分隔，例如 `day19,pilot_repair`。每个声明的 schema 必须有包含 `artifact_sha` 的 `repair_runs` 表；任何查询失败都会终止盘点，不会把失败误当作没有引用。工具不会读取 `.env`，也不会回退到 `DATABASE_URL`。它设置只读连接和只读事务，并检查实际数据库名称中独立的 `test` 标记；名称检查只是误用防护，不证明数据库一定没有业务数据。

输出仅包含分类文件数、普通文件字节数及引用的唯一 SHA 数量（不是 Run 行数），不包含目录路径、文件名、Run ID、内容、连接串或 Token。符号链接和其他非普通文件不读取内容、大小按零计。合法 SHA 文件名还需要内容哈希验证；损坏制品单独计数。`observed_without_valid_file` 表示本次目录观察中没有对应有效文件，可能由损坏、缺失或并发发布导致。

引用分别在目录扫描之前和之后观察。`reference_appeared_during_scan` 和 `reference_disappeared_during_scan` 保留已观察到的变化；`currently_unreferenced` 仅表示这两次查询没见到引用。上传与数据库引用提交之间存在间隙，新的引用也可能在工具退出后出现。因此，报告始终声明：

- `read_only: true`
- `reference_completeness_proven: false`
- `deletion_allowed: false`
- `age_used_for_eligibility: false`

调用者提供的 schema 清单无法自动证明覆盖全部发布者、其他数据库或存储消费者。两次读取也不是数据库和文件系统的一致快照。文件年龄不会用于推导删除资格，尤其不能用不稳定的系统时钟决定删除。

盘点结束不需要清理操作。真正的制品 GC 仍需发布 pin、完整引用登记及跨发布者协调协议，当前未实现。
