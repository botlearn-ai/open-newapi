# 集成账户逐请求结算（准备阶段）

本协议为 Course 额度分桶提供单账户供应商账单。当前实现可本地测试，尚未达到生产开启条件。
旧账户默认 `billing_protocol=""`，保留原行为。新账户可在集成 provision 请求中显式设置
`billing_protocol="bounded-v1"`；同一外部身份不能通过重复 provision 更换协议。
历史账户转换尚未实现，必须先解决跨进程旧请求排空与期初核对。

## 合同

全部控制请求复用现有 IntegrationAuth：`X-Integration-ID` 与服务端 Bearer。
用户模型 Token 无权创建、取消或查询其他集成账户的授权。

| 接口 | 请求体 | 结果 |
| --- | --- | --- |
| `POST /api/integrations/requests` | external_user_id, request_key, path, body_sha256, max_quota, quota_per_usd | 校验单位与当前配置一致，再幂等创建授权；相同 key 不同参数返回 409 |
| `POST /api/integrations/requests/query` | external_user_id, request_key | 查询持久状态和金额；若为 settling，按已保存金额幂等补结算 |
| `POST /api/integrations/requests/cancel` | external_user_id, request_key；取消尚不存在的授权还须提供原 path, body_sha256, max_quota, quota_per_usd | ready 原子取消；不存在则创建取消记录；running/settling 返回 409 |

模型请求使用已配置用户 Token，并附带 `X-Integration-Request-Key`。
正文摘要绑定客户端发送到网关的原始字节，路径只支持 `/v1/chat/completions`、`/v1/responses`、
`/v1/messages`。授权单次执行；受控请求关闭网关内部重试，包括渠道错误和 502。
响应丢失后查询原授权，不重发模型。

`quota_per_usd` 固定为授权时快照，必须是 1～1000000 内的整数且整除 1000000，
从而每个整数 quota 都能精确表达为六位 USD。配置变化后旧授权拒绝开始执行，允许查询和取消。
已开始执行的请求在文本、工具附加费、音频和缓存费用换算中继续使用原单位；阶梯计费快照必须与授权单位一致。

| state | 金额含义 | 恢复方式 |
| --- | --- | --- |
| ready | 未扣余额，已保存授权 | 执行一次或原子取消 |
| running | 钱包和 Token 同事务扣除 max_quota；最终金额未知 | 查询；不能按超时归零 |
| settling | actual_quota 已持久化，余额尚未完成结算，charged_quota=0 | 查询原请求触发幂等补结算；不能当作最终账单释放预留 |
| settled | actual_quota = charged_quota，差额已退回 | 同一金额重复结算无副作用 |
| cancelled | 明确未执行，金额为零 | 保留原授权避免重用 |
| disputed | actual_quota 超出上限，保留完整实际金额和原预留 | 新调用暂停，等待有审计的人工裁决 |

扣费真相保存在主数据库 `integration_requests`，不依赖消费日志开关或独立 LOG_DB。
先提交不可改额的 settling 事实，再将余额、Token、用户累计用量和终态同事务提交。
钱包或 Token 更新失败会回滚资金变动并保留 settling；重启后由查询补结算。
退款只恢复有余额且未过期的耗尽令牌，保留管理员停用状态。鉴权标记耗尽时条件更新，避免旧读取覆盖退款。
普通失败、流式取消、缺少用量时保留 running，不自动退款。
原子查询和取消解决“还没开始”与“已经执行”的竞争；无法确认时继续保留预留。
授权尚未到达时，取消接口在同一账户锁下保存原参数和 cancelled 记录；迟到的授权与模型请求无法执行。
404 本身不证明可以退款，Course 必须拿到该取消记录。取消允许使用原单位，即使当前配置已经变化。

## 必须补齐的开启门槛

- 当前限制账户追加扣款，不能证明上游所有模型/工具的实际成本在执行前可控；需要逐类限制最大输出、工具和收费项。
- 供应商适配器可能按本地规则补算用量；这里的 actual 指 NewAPI 账单金额，不能宣称为供应商原始发票。
- 若数据库在首次保存最终金额前已不可写，或进程在该持久化前退出，仍保留 running，不能凭空恢复供应商用量。
  running/disputed 的供应商证据查询和有审计的人工裁决入口仍待实现，不支持改 SQL 释放预留。
- 历史切换、MySQL 迁移/并发、真实上游流式取消和跨服务整体验收待完成。
- 已启用账户只能回退到理解本协议的版本；旧版本会绕开约束，禁止直接回退。

## 本地验证

SQLite：`go test ./service ./controller ./middleware -run 'TestIntegration|TestBotcord' -count=1`。
PostgreSQL：向 `TEST_INTEGRATION_POSTGRES_DSN` 提供专用测试数据库 URL，然后执行
`go test ./service -run TestIntegrationRequest -count=1`。测试创建并删除独立 schema。
不要指向生产数据库。并发测试在未提供该变量时明确跳过。

覆盖正文/金额绑定、重复消费拒绝、并发最终结算、取消、未知保留、事务原子性、
超额原额记录/暂停、精度限制，以及无授权或其他 Token 绕过。
完整 controller 测试的模型列表失败在基准 `2cb1f571` 同样复现，专项通过不等于全仓库测试通过。
