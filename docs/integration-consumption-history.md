# 集成账户消费历史

`POST /api/integrations/consumption` 复用 `IntegrationAuth` 和限流，仅供可信服务端查询。
请求包含 `external_user_id`、`limit`（必填，1～50）、`before_id`（默认 0，即第一页）。
身份由已认证 integration 命名空间和 external_user_id 共同决定，不能指定内部 user_id。

响应 `data` 包含 `external_user_id`、`quota_per_usd`、`entries` 和 `next_before_id`。
每条记录只包含 `id`、`created_at`（Unix 秒）、`type`（2 扣费、6 消费退回）、
`model_name`、`quota`、`prompt_tokens` 和 `completion_tokens`。
quota 为非负金额，方向由 type 决定。USD = quota / quota_per_usd。
历史日志未保存独立换算快照，沿用 NewAPI 日志的统一 quota 单位；不得改变该单位后
把旧记录按新单位解释。接口不返回请求正文、IP、密钥、渠道、内部错误、other 或用户资料。

记录来自 LOG_DB，覆盖该集成账户的所有令牌（包含已轮换和删除的令牌），仅返回仍保留的日志。
使用实际日志 ID 倒序和 `id < before_id` 查询，额外读取一条判断是否还有下一页。
`next_before_id=null` 表示结束；翻页期间新增消费不会挤走原有页面记录。
不承诺已清理日志的恢复，也不把调用估算成本补成实际账单。

查询没有开户、认领旧映射、补发额度、修复令牌或写入账本的副作用。未建立集成映射或停用用户返回 404；
零余额、令牌过期/轮换/删除不阻止读取账户历史。错误参数返回 400，读取故障返回 503，均为固定脱敏信息。
响应禁止缓存。现有 balance、token log 和用户日志接口契约保持不变。

## 消费汇总与课程归属

`POST /api/integrations/consumption/summary` 使用相同鉴权、账户校验和错误约定，
请求只包含 `external_user_id`。响应 `data` 包含该外部 ID、`quota_per_usd`、
`record_count`、`net_quota`、`first_at`、`last_at`。

单次数据库聚合统计该账户全部保留的消费与退回日志，不受分页或 1,000 条限制。
`net_quota = SUM(消费 quota) - SUM(退回 quota)`，允许零值和净退回负值；
`record_count` 包含上述两类记录，起止时间为 Unix 秒。
没有记录时返回 0 条、0 净额及 null 起止时间。查询不读取日志内容、不返回模型或 token 明细，
不写入余额或账本。读取或求和失败返回 503，不能替换为零。

汇总新增 groups，每组包含 scope、record_count、net_quota、first_at、last_at。
同一 SQL 按 scope 聚合，总字段由分组结果相加；不另发查询，避免并发消费导致不一致。
scope 为课程 UUID、general（其他消费）或空字符串（存量历史）。

首次日志库迁移创建 IntegrationConsumptionCutover 单行，冻结当时最大 logs.id。
旧记录（id <= LastLogId）始终属于历史，后续无归属记录也归 general。
重复迁移不移动分界，兼容部署期间旧进程空归属写入。迁移只新增 logs.billing_scope 列和分界表，
不改额度、不回写旧消费。独立日志库和共库均从 InitLogDB 的主节点迁移入口执行。
禁用应用自动迁移的共库环境使用固定迁移程序：新增不可变清单 20261015_0002_course_spending，
在既有数据库锁内执行，并验证清单校验和、字段和分界。旧版清单内容及校验和保持不变。
独立日志库须先通过其主节点迁移入口迁移；固定迁移程序针对 SQL_DSN 指向的共库。
迁移及查询有 SQLite/PostgreSQL/MySQL 行为测试。
日志清理仍影响可查询金额；固定边界不代表备份或恢复已清理日志。

可信集成可以签发 isc1 短期归属凭据作为模型 Authorization Bearer。
格式：isc1.<base64url JSON无padding>.<base64url HMAC-SHA256无padding>。
签名输入 integration-scope-v1: 加编码后的 JSON，密钥来自既有 INTEGRATION_CLIENTS 中该集成的 secret。
字段为 integration、user（外部用户）、course（标准 UUID 或 general）、key（原模型 API key）、expires（Unix 秒）。
拒绝超过 4096 字节、未知集成、无效签名、非标准 course、过期或超过当前时间一小时的凭据。
校验集成映射与原令牌用户一致后，原 TokenAuth 的限额、停用、IP、模型、渠道和受控扣费门禁继续执行。
身份和归属来自受信服务端；归属签名不授予新的扣费能力。不得把签名凭据返回浏览器、写日志或持久化。
RecordConsumeLog 保存已验证归属，无归属默认 general。异步任务账单保留 general；该凭据用于同步课程模型调用。

发布顺序为 NewAPI（包括日志库迁移）→ Course API/工作进程 → Web。
旧版 NewAPI 不识别 isc1，禁止先发 Course；回滚先停用 Course 签发，再回退 NewAPI，保留扩展列及分界。
旧汇总字段与逐条分页接口保持兼容。Course 新钱包仍使用独立分桶账本；两种资金来源不混合累加。
没有新增环境变量，使用既有日志开关、保留策略和用户索引。

归属凭据只接受 `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 和模型发现 `/v1/models[/…]`，
其他路由在解析前拒绝，避免异步任务的后续结算丢失课程归属。原 API key 的路由能力保持不变。
