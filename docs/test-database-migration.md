# 中国测试环境数据库迁移

中国 Test 的长期服务保留 `NODE_TYPE=slave`。启动服务不执行结构迁移。
`Deploy China Test` 先发布同一源码提交的运行镜像与 `cn-test-<SHA>-migrate`
迁移镜像，再调用 DevOps 已登记的固定任务。迁移失败时不推广 `test`，也不重启服务。

迁移镜像的唯一命令是 `newapi-migrate`，不接受参数、外部 SQL 或用户指定的迁移文件。
固定任务只投影 `SQL_DSN`，并设置 `VERIFY_SQL_MIGRATION_MANIFEST=true`。
连接必须显式提供，支持 PostgreSQL URL、MySQL TCP DSN 和 `sqlite://` 路径，空值拒绝执行。
中国 Test 使用既有 PostgreSQL 数据库；另外两种数据库用于保持项目兼容。

首个版本 `20261014_0001_integration_billing` 要求既有用户、令牌、集成账户与操作表已存在，
只新增 `billing_suspended`、`billing_protocol`、`integration_requests` 及必要索引。
旧账户默认保持未暂停、空计费协议。迁移不更新用户余额、充值订单或消费日志，也不切换旧账户协议。
新增 `newapi_schema_migrations` 仅记录版本、完整 SQL 清单 SHA-256 与执行时间。

清单包含各方言的固定 SQL 和冻结的 V1 表定义。表的主键 SQL 由锁定版本的 GORM 生成；
生成后的 SQL 同样参与校验和。成功前核对字段类型、非空约束、默认值、长度、主键与索引。
未知版本、校验和不符、既有结构不兼容都拒绝执行；已执行版本也会重新核对实际结构。
未来变更追加新的版本及迁移，不修改已发布的 V1 SQL、表定义或其 SQL 生成依赖。

PostgreSQL 使用事务与 advisory lock（数据库锁），SQLite 使用写事务。
MySQL 使用连接级命名锁；其 DDL 隐式提交，因此每条操作检查当前结构，失败后可在同一清单下重跑。
每种数据库均在结构核验成功后才写入迁移账本。错误日志只输出固定分类，不输出连接串或驱动原始错误。

生产环境未接入此发布路径。首次 Test 接入顺序是：合并应用与 DevOps 配置，发布 Test 专属 IAM、
RBAC、准入与单键 Secret 投影，验证前置资源后启用执行器，再运行同一应用提交的测试部署。
不要只移动旧结构基线，也不要临时把长期服务改成主节点来触发 AutoMigrate。

## 请求结算审计表

`20261110_0003_request_reconciliation` 在既有两项清单之后创建
`integration_request_reconciliations`，保存请求、操作人、最终费用证据和原因，
并用 `request_id` 唯一索引防止重复审计决定。迁移沿用原锁、事务、校验和及重跑路径，
不改写已有请求、用户余额或审计记录。冻结表定义在 `migration/reconciliation.go`，
实际发送的完整 SQL 参与校验和，字段与索引验证通过后才登记成功。

这是 #25 在中国 test 验收发现的迁移遗漏：长期服务为 `NODE_TYPE=slave`，
仅注册 GORM AutoMigrate 模型不会创建正式表。发布必须运行独立迁移镜像；
回滚应用时保留新增表和迁移账本。旧迁移命令会拒绝新版本账本，回滚时不得运行旧迁移命令。
跟踪：https://github.com/botlearn-ai/botlearn-course/issues/2185。

## 用量看板小时桶

`20261120_0004_quota_data_bucket` 为 `quota_data` 增加
`(user_id, username, model_name, created_at)` 复合唯一索引。应用以单条 SQL 将请求数、额度与
Token 数的增量写进对应小时桶。先使用同一提交的固定迁移镜像，再升级长期服务及增加副本。
迁移持锁创建索引，运行前核对历史数据量和数据库写入窗口。

迁移检查历史重复键、空键和空计数。存在异常时停止发布，原始统计行保留，不登记此版本成功。
运维可先通过获准的只读通道检查异常桶数，避免输出用户和模型明细：

```sql
SELECT COUNT(*) AS duplicate_buckets
FROM (
    SELECT 1 FROM quota_data
    GROUP BY user_id, username, model_name, created_at
    HAVING COUNT(*) > 1
) AS duplicates;

SELECT COUNT(*) AS incomplete_buckets
FROM quota_data
WHERE user_id IS NULL OR username IS NULL OR model_name IS NULL OR created_at IS NULL
    OR count IS NULL OR quota IS NULL OR token_used IS NULL;
```

发现冲突时，另行保护受影响的原始记录，再核对消费日志在对应时段的覆盖、保留及日志库位置。
旧逻辑会对同一桶的每条重复行累加后续增量，直接求和可能高估。消费日志完整且来源可核验时，
可据此重算请求数、额度与 Token 数。证据缺失的时段保留历史记录，并单独评审处理范围后重跑迁移。
此迁移只改用量看板索引，钱包、Token 余额和受控请求结算数据由既有事务维护。
如应用回滚，先收回额外副本，再回滚应用镜像并保留索引及迁移账本。后续仍使用支持本版本的
迁移镜像，旧迁移命令会拒绝带有新版本的账本。

## 验证

`go test ./migration ./cmd/migrate -count=1` 默认执行隔离 SQLite 检查。
真实数据库测试只接受回环地址及数据库名 `newapi_migration_test`，由本任务或 CI 创建专用空数据库，
通过 `TEST_MIGRATION_POSTGRES_DSN`、`TEST_MIGRATION_MYSQL_DSN` 提供连接。测试会清理该测试库中的夹具表。
未设置连接时会明确跳过对应数据库；不得把跳过报告为通过。

覆盖数据保留、重复执行、主键生成、请求键唯一性、异常历史、结构和索引漂移，以及 PostgreSQL/MySQL
并发执行。SQLite 的并发写入可能返回 busy；受限任务本身串行执行，服务端锁测试由两种服务器数据库覆盖。
