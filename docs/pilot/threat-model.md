# 信任与部署边界

可信：Linux 内核、PostgreSQL 管理员、迁移/身份注册者、登记仓库配置、控制面。数据库管理员和宿主管理员可越过本系统边界。

不可信：请求体/问题、模型输出、仓库源码与测试、数据库描述与业务文本、工具日志、用户传入 ID。防护目标是跨团队数据泄漏、越权执行、旧 Owner 提交、预算超支和资源无界增长。

认证结果决定 principal/team/tenant。对象 ID 和模型字段不授予权限。默认不采集源码、Prompt、环境或供应商原始错误；审计仅记录结构化摘要。秘密不进子进程/容器。

容器共享宿主内核；没有运行容器的实际拒绝与资源证据就不能称安全沙箱。默认禁止网络、宿主挂载、任意镜像参数；受控候选快照与权威持久目录分离。单 PostgreSQL 为单点，模型远端超时可能已经收费，无 exactly-once 计费保证。

两项目保持独立闭环，本轮不通过跨项目集成掩盖各自缺口。依赖材料核对：PostgreSQL RLS https://www.postgresql.org/docs/current/ddl-rowsecurity.html 、LangGraph https://docs.langchain.com/oss/python/langgraph/persistence 、OTel https://opentelemetry.io/docs/concepts/signals/traces/ 、Docker https://docs.docker.com/engine/containers/resource_constraints/ 。
