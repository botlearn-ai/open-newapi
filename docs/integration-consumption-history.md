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

## 历史消费汇总

`POST /api/integrations/consumption/summary` 使用相同鉴权、账户校验和错误约定，
请求只包含 `external_user_id`。响应 `data` 包含该外部 ID、`quota_per_usd`、
`record_count`、`net_quota`、`first_at`、`last_at`。

单次数据库聚合统计该账户全部保留的消费与退回日志，不受分页或 1,000 条限制。
`net_quota = SUM(消费 quota) - SUM(退回 quota)`，允许零值和净退回负值；
`record_count` 包含上述两类记录，起止时间为 Unix 秒。
没有记录时返回 0 条、0 净额及 null 起止时间。查询不读取日志内容、不返回模型或 token 明细，
不写入余额或账本。读取或求和失败返回 503，不能替换为零。

Course 旧账户使用汇总接口展示一条历史记录，新 credits 账户使用平台分桶账本；
两种来源不混合累加。旧逐条分页接口保持兼容。汇总在查询时计算，不新增结算截止时间。
发布时先部署 NewAPI，再部署依赖汇总接口的 Course。旧版 NewAPI 上 Course 应显示可重试的加载失败。
无新增环境变量和数据库结构变更；复用 logs 的 user_id/id 索引。
