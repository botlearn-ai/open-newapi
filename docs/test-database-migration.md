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

## 验证

`go test ./migration ./cmd/migrate -count=1` 默认执行隔离 SQLite 检查。
真实数据库测试只接受回环地址及数据库名 `newapi_migration_test`，由本任务或 CI 创建专用空数据库，
通过 `TEST_MIGRATION_POSTGRES_DSN`、`TEST_MIGRATION_MYSQL_DSN` 提供连接。测试会清理该测试库中的夹具表。
未设置连接时会明确跳过对应数据库；不得把跳过报告为通过。

覆盖数据保留、重复执行、主键生成、请求键唯一性、异常历史、结构和索引漂移，以及 PostgreSQL/MySQL
并发执行。SQLite 的并发写入可能返回 busy；受限任务本身串行执行，服务端锁测试由两种服务器数据库覆盖。
