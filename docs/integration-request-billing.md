# 集成账户逐请求结算（准备阶段）

本协议为 Course 额度分桶提供单账户供应商账单。当前实现可本地测试，尚未达到生产开启条件。
旧账户默认 `billing_protocol=""`，保留原行为。新账户可在集成 provision 请求中显式设置
`billing_protocol="bounded-v1"`；同一外部身份不能通过重复 provision 更换协议。
历史账户由 Course 的一次性切换脚本把 `billing_protocol` 改为 `bounded-v1` 并按当时余额建立期初；
切换时仍在执行的旧请求可能留下小额偏差，由 Course 侧预留封顶容忍。

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
| unresolved | 请求已结束但最终费用缺少证据；保留原预扣 | 核对供应商证据后受控结算；不按错误码或超时归零 |
| settling | actual_quota 已持久化，余额尚未完成结算，charged_quota=0 | 查询原请求触发幂等补结算；不能当作最终账单释放预留 |
| settled | actual_quota = charged_quota，差额已退回 | 同一金额重复结算无副作用 |
| cancelled | 明确未执行，金额为零 | 保留原授权避免重用 |
| disputed | actual_quota 超出上限，保留完整实际金额和原预留 | 新调用暂停；该账户下一次集成充值入账时恢复 |

扣费真相保存在主数据库 `integration_requests`，不依赖消费日志开关或独立 LOG_DB。
先提交不可改额的 settling 事实，再将余额、Token、用户累计用量和终态同事务提交。
钱包或 Token 更新失败会回滚资金变动并保留 settling；重启后由查询补结算。
退款只恢复有余额且未过期的耗尽令牌，保留管理员停用状态。鉴权标记耗尽时条件更新，避免旧读取覆盖退款。
调用失败收尾 Refund 时把 running 标记为 unresolved，保留预扣；流式取消或缺少用量只有执行该收尾路径才会得到此标记。进程崩溃或该写入失败仍可能保留 running。HTTP 500 和错误日志中的零 quota 都不是最终零费用证据。
原子查询和取消解决“还没开始”与“已经执行”的竞争；无法确认时继续保留预留。
授权尚未到达时，取消接口在同一账户锁下保存原参数和 cancelled 记录；迟到的授权与模型请求无法执行。
404 本身不证明可以退款，Course 必须拿到该取消记录。取消允许使用原单位，即使当前配置已经变化。

## 限流

集成接口按集成 ID 限流（`INTEGRATION_RATE_LIMIT` / `INTEGRATION_RATE_LIMIT_DURATION`，默认 180 秒 30000 次），
不再经过 `/api` 全局按 IP 限流：合作方所有用户的授权、查询和余额调用共用同一出口地址，每次模型请求
会产生多次集成调用。凭据校验失败的请求仍按来源 IP 计入 `GLOBAL_API_RATE_LIMIT`，防止从公网猜测密钥。

## 必须补齐的开启门槛

- 当前限制账户追加扣款，不能证明上游所有模型/工具的实际成本在执行前可控；需要逐类限制最大输出、工具和收费项。
- 供应商适配器可能按本地规则补算用量；这里的 actual 指 NewAPI 账单金额，不能宣称为供应商原始发票。
- 若数据库在首次保存最终金额前已不可写，或进程在该持久化前退出，仍保留 running，不能凭空恢复供应商用量。
  running/unresolved 可使用下述证据核对入口。disputed 的额外扣款与争议处置不在本次范围，不支持改 SQL 释放预留。
- MySQL 迁移/并发已完成本地验证；历史切换、真实上游流式取消和部署后跨服务验收待完成。
- 已启用账户只能回退到理解本协议的版本；旧版本会绕开约束，禁止直接回退。

## 本地验证

SQLite：`go test ./service ./controller ./middleware -run 'TestIntegration|TestBotcord' -count=1`。
PostgreSQL：向 `TEST_INTEGRATION_POSTGRES_DSN` 提供专用测试数据库 URL，然后执行
`go test ./service -run TestIntegrationRequest -count=1`。测试创建并删除独立 schema。
不要指向生产数据库。并发测试在未提供该变量时明确跳过。

覆盖正文/金额绑定、重复消费拒绝、并发最终结算、取消、未知保留、事务原子性、
超额原额记录/暂停、精度限制，以及无授权或其他 Token 绕过。
完整 controller 测试的模型列表失败在基准 `2cb1f571` 同样复现，专项通过不等于全仓库测试通过。

## 失败请求的证据核对

管理员只读查询复用现有用户管理权限与目标用户角色边界：

- `GET /api/user/:id/integration-requests?before=<id>`：该用户最多 50 条，按 ID 降序分页。
- `GET /api/user/:id/integration-requests/:request_id`：请求及已有核对记录。

这两个 GET 不执行 settling 补结算，不返回模型令牌、原始正文或正文摘要。
写入接口 `POST /api/user/:id/integration-requests/:request_id/reconcile` 只接受 root 浏览器登录会话，
拒绝管理员访问令牌、普通管理员、集成凭据与模型令牌。沿用登录头及敏感操作限流。
请求体字段：

| 字段 | 要求 |
| --- | --- |
| provider_request_id | 必须精确匹配该请求，防止核对错单 |
| actual_quota | 必须显式提供非负整数；0 是经证据确认的零消费，缺省不能当成 0 |
| evidence_ref | 8～1000 字节的持久证据编号；不得包含密码、令牌或带签名 URL |
| reason | 8～2000 字节，说明依据与核对结果 |

入口只处理 running/unresolved；running 包含旧版本失败遗留，也可能是真正在执行的请求。
操作人必须先确认请求已结束并取得可信最终费用，不能凭等待时长执行本命令。
本入口记录人工裁决依据，不自动验证外部账单真实性。

主库新增 `integration_request_reconciliations`。正常和快速 AutoMigrate 均注册该表；
`request_id` 唯一，每个请求保留一次不可改额的核对记录。记录操作人、上游请求号、金额、依据和时间，
并与 settling 金额在一个事务内提交。没有审计记录就不能提交人工结算金额。
随后复用既有幂等结算：钱包失败保留 settling，原请求查询可续办，无需再次模型调用。
同一操作人以相同内容重复提交无副作用；换金额、依据、原因或操作人返回冲突。
迟到的正常账单也只能复用同一金额。超上限进入既有 disputed 流程，保留预扣并暂停该账户新调用。

发布顺序：先发布 Course 对 unresolved 的读取与 `credit_settlement_needs_review` 处理，再发布网关及审计表。
Course 收到该状态即停止无效的自动重试，保留额度等待核对；网关确认后，使用 Course 原结算任务的
既有管理员重试入口收取最终账单。重试只查询原请求，不重发推理请求。
生产历史请求的证据收集、人工裁决、余额恢复与发布须单独授权。

回退保留审计表、unresolved/settling 行和已经完成的账单；不得删表、批量改状态或回退到不理解受控账单的版本。
正常部署先在隔离数据库演练 AutoMigrate 和重放；本地测试不代表生产已经迁移。

MySQL 本地测试使用专用无业务数据库的实例：
`TEST_INTEGRATION_MYSQL_DSN='root@tcp(127.0.0.1:<port>)/?parseTime=true' go test ./service -run TestIntegrationRequest -count=1`。
每项用例创建并删除自己命名的测试数据库。不要提供生产实例。


2026-11-10 本地复测：SQLite 集成专项 57 项通过、4 项数据库并发用例跳过；
PostgreSQL 17 与 MySQL 8 各 31 项（含子用例）通过，覆盖并发和审计表迁移重放。
MySQL 默认可重复读下，在账户锁等待后使用当前读取得已提交的核对记录和取消墓碑，
避免旧快照让幂等命令误报冲突或找不到记录。首轮失败保留为回归证据。
真实上游调用、生产迁移、历史余额恢复及部署后跨服务整体验收仍未执行。

## Course 跨服务本地合同测试

2026-11-10：Course 的真实 HTTP 客户端、额度账本与本仓库控制器/计费服务各 1 项通过。
第一笔模型失败保留预留，第二笔独立请求成功结算 247 quota，第一笔经明确的零费用证据
核对后释放预留；重复核对、重复查询、重复 Course 结算均不重复扣费，推理入口恰好调用两次。
登录身份与上游模型响应由测试夹具创建，鉴权中间件、事务与结算代码实际运行。
这不证明真实供应商账单、生产登录或部署后的接口可用性。

在专用本地测试环境运行，先启动网关测试端：

```bash
TEST_COURSE_CONTRACT_ADDR=127.0.0.1:19364 go test ./controller \
  -run '^TestIntegrationCourseContractHTTPPeer$' -count=1 -v -timeout 10m
```

再在配套 Course 工作树的 services/course-api 运行：

```bash
TEST_NEWAPI_CONTRACT_URL=http://127.0.0.1:19364 NO_PROXY=127.0.0.1,localhost \
  LANGFUSE_TRACER_ENABLED=false SENTRY_DSN='' \
  uv run pytest tests/test_newapi_cross_service.py -q -n 0
```

测试端使用独立内存 SQLite；测试完成后由 Course 请求关闭，普通 go test 默认跳过此协作入口。
若在容器内运行，监听容器 0.0.0.0 并仅向宿主 127.0.0.1 发布端口；不得暴露到公网。
