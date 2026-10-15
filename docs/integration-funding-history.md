# 集成账户历史加额查询

`POST /api/integrations/funding` 复用 `IntegrationAuth` 和限流，仅供可信服务端调用。
请求为 `external_user_id`、`before_id`（默认 0）和 `limit`（1～50）。
按认证集成与外部身份查找既有账户；查询不创建账户、修复令牌或增加余额。

响应 `data` 包含 `external_user_id`、`quota_per_usd`、`entries`、`next_before_id`。
按操作主键倒序，每项只包含 `id`、`created_at`（Unix 秒）、`operation`、`quota`。
仅自己的 `topup` 附带 `idempotency_key_hash`，供调用方与已有赠送凭据去重；管理员操作不返回该摘要。
调用方不得向浏览器透传摘要。翻页使用上一页的 `next_before_id`，到末尾为 null。

- 包含本集成 `topup` 和映射到该账户的 `admin_topup`。
- 排除只修复令牌额度的 `admin_token_topup`，以及其他账户、其他集成和非正额操作。
- 早期开通赠送未保存金额；管理员文本日志也缺少可靠的结构化金额，因此不能据此推算赠送。
- `quota_per_usd` 是当前额度单位，与账户余额一致；历史操作没有单位快照，不代表原始美元付款金额。
- `topup` 不足以证明课程、支付或赠送来源；调用方匹配不到自有凭据时应显示“历史加额”。
- 此查询不汇总或重新发放资金，不替代余额、退款或新账本迁移。

响应禁止缓存。非法请求 400、账户不存在或停用 404、上游数据不可读 503；错误不返回 SQL。
无数据库结构或环境变量变更。先部署 NewAPI，再部署调用方；回滚调用方不影响账户余额。
