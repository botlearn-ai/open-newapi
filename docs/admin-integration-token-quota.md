# 管理员补充集成令牌额度

集成账户的用户余额和关联令牌额度分别管理。在用户管理中增加用户余额后，可通过「编辑用户 → 集成令牌额度」查看关联令牌，并单独增加其额度。本功能仅管理 `IntegrationAccount` 中关联的令牌，不展示密钥。

## 认证与权限

两个接口均使用现有 `AdminAuth`：管理员网页登录会话，或管理员个人访问凭证；同时携带 `New-Api-User`。普通管理员只能管理角色低于自己的用户，超级管理员沿用现有用户管理权限。集成密钥不能代替管理员认证。

## 查看关联令牌

`GET /api/user/:id/integration-tokens`

成功响应的 `data` 包含 `user_quota` 和 `tokens` 数组。每个令牌包含 `account_id`、`integration_id`、`token_id`、`name`、`remain_quota`、`unlimited_quota`、`status` 和 `expired_time`。额度单位是内部 quota，不是美元。已删除或归属不匹配的令牌不会返回。

## 单独增加令牌额度

`POST /api/user/:id/integration-tokens/:token_id/quota`

请求头：`Idempotency-Key: <本次操作唯一编号>`。

```json
{"quota": 2500000}
```

默认 `QuotaPerUnit = 500000` 时，上例为增加 $5；实际换算应依据站点配置。只增加 `remain_quota`，不修改用户余额、`used_quota`、有效期或无限额度设置。只有额度耗尽、仍在有效期内且加额后余额为正的令牌会自动恢复启用；停用、过期的令牌不会自动恢复。无限额度令牌不支持加额。

成功响应：`{"success":true,"message":"","data":{"replayed":false}}`。同一管理员使用相同幂等编号重试相同请求，不会重复加额，返回 `replayed: true`；相同编号用于不同目标或额度时返回 HTTP 409。缺少编号返回 HTTP 400。调用方也必须检查响应中的 `success`，部分业务错误沿用 HTTP 200 的项目约定。

充值使用数据库原子增量和事务。幂等记录与加额一起提交，记录管理员 ID；管理日志另记录管理员、用户、令牌和增加额度。提交后失效该用户令牌缓存。日志存储或缓存服务失败沿用项目错误日志机制，幂等记录仍可用于核查。

`IntegrationOperation` 新增 `admin_id` 字段，默认值为 0，既有集成操作无需回填；启动时通过现有 GORM 自动迁移新增该列，兼容 SQLite、MySQL、PostgreSQL。

这是补齐已经充值到用户余额的令牌额度的入口。正常的双额度集成充值仍使用现有 `/api/integrations/topup`，不能用于重复补齐本次已入账的用户余额。
