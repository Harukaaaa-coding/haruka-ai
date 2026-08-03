# 发布、迁移与备份运行手册

本手册面向将 GopherAI 部署到非本机环境的操作人员。它假定 MySQL、Redis Stack 和 RabbitMQ 由 Compose 管理，API 可以在宿主机或同一 Compose 网络中的容器运行。

## 发布顺序

1. 确认当前 API/worker 正常，并先生成一份可校验备份。
2. 停止或摘流全部 API/worker 实例，避免迁移期间继续写入。
3. 发布新二进制，但不要先启动 API。
4. 使用新二进制执行 `go run ./cmd/migrate -command up`。
5. 执行 `go run ./cmd/migrate -command verify`；只有它成功后才启动 API。
6. 请求 `/readyz`，确认 MySQL、Redis 和需要的 worker 状态正常后再恢复流量。

迁移记录保存在 MySQL 的 `gopherai_schema_migrations` 表。迁移命令持有 MySQL advisory lock，同一数据库上并发运行时会等待 30 秒。已记录的迁移不会重复执行；如果历史记录的版本、名称或校验和与当前二进制不一致，命令会拒绝继续，而不是猜测应如何修复。

```powershell
# 查看当前版本；尚未初始化的旧数据库会明确报错。
go run ./cmd/migrate -command status

# 对旧 AutoMigrate 数据库执行一次兼容基线，或应用新版本。
go run ./cmd/migrate -command up

# 发布门禁：不写入任何 DDL，只验证版本完整性。
go run ./cmd/migrate -command verify
```

命令会读取通常的 `config/config.toml` / `config/config.local.toml` 和已导入的 `GOPHERAI_*` 环境变量。它不会自动读取 `.env`；生产环境应由部署平台注入秘密。`-timeout` 默认是 5 分钟，可按维护窗口调整。

## 兼容期与启动策略

`GOPHERAI_DB_MIGRATION_MODE` 只有两个安全模式：

| 值 | 行为 | 使用场景 |
| --- | --- | --- |
| `compatibility`（也接受 `auto`） | API 启动时运行版本化迁移，作为旧数据库的一次性过渡桥。 | 仅限本机临时兼容；不要作为常规启动方式。 |
| `verify`（也接受 `manual`） | API 只验证迁移已完成；有缺失、未知或被改写的历史即拒绝启动。 | 默认值；生产、预发布、多个实例。 |

未设置时，所有环境都默认使用 `verify`。推荐的 `scripts/run-backend.ps1` 会在启动 API 前单独运行 `cmd/migrate up`，因此不会降低本机开发便利性。生产部署应显式设置：

```powershell
$env:GOPHERAI_ENV = "production"
$env:GOPHERAI_DB_MIGRATION_MODE = "verify"
```

生产或预发布环境不能用 `compatibility` 覆盖该门禁；需要迁移时应先运行独立的 `cmd/migrate`，再启动 API。

当前第一个迁移是旧版 GORM 自动建表的受控基线，只在 `cmd/migrate` 或兼容模式中执行并被记录一次；API 不再无条件调用 `AutoMigrate`。后续结构变化必须新增一个有序迁移，不得修改已经发布的迁移定义或其版本。

## 健康检查与监控

负载均衡器应使用 `GET /livez` 判断进程是否存活，使用 `GET /readyz` 判断是否可以接收流量；开始优雅停机后 `/readyz` 会返回 `503`，应先完成摘流。`GET /metrics` 输出 Prometheus 0.0.4 文本格式，包含按路由模板聚合的 HTTP 请求数、延迟、进行中请求和健康状态。它不包含请求 URL、请求体、Cookie、授权头或健康检查的底层错误详情。默认只有 loopback 可以访问；对远程 scraper 设置 `GOPHERAI_METRICS_TOKEN`，并在 Prometheus 的 `authorization` 配置中使用 `Bearer <token>`。健康 gauges 使用短 TTL 缓存，避免高频采集放大依赖探测负载。

每个响应携带 `X-Request-ID`。排查一次请求时，可将该值与 JSON 访问日志中的 `request_id` 对照；访问日志同样不会记录查询参数、Cookie、授权头或请求体。

## Compose 安全基线

`docker-compose.yml` 是本机开发文件：端口仍可用于宿主机后端，但均绑定到 `127.0.0.1`，不会暴露给局域网。镜像固定到 MySQL `8.4.11`、Redis Stack `7.4.0-v8` 和 RabbitMQ `4.3.4-management`。

生产使用 `docker-compose.production.yml`。它要求提供 MySQL、Redis 与 RabbitMQ 的真实 `GOPHERAI_*` 秘密，且不会发布 RabbitMQ 管理界面：

```powershell
# 先通过 Secret 管理或当前部署进程设置以下变量；不要提交到 Git。
$env:GOPHERAI_MYSQL_ROOT_PASSWORD = "..."
$env:GOPHERAI_MYSQL_USER = "gopherai"
$env:GOPHERAI_MYSQL_PASSWORD = "..."
$env:GOPHERAI_REDIS_PASSWORD = "..."
$env:GOPHERAI_RABBITMQ_USERNAME = "gopherai"
$env:GOPHERAI_RABBITMQ_PASSWORD = "..."

docker compose -f docker-compose.production.yml config
docker compose -f docker-compose.production.yml up -d
```

如果 API 在宿主机运行，它继续通过 `127.0.0.1` 访问基础设施；如果 API 容器化，则应在同一个 Compose 网络中使用 `mysql`、`redis`、`rabbitmq` 服务名，不应新增公网端口映射。镜像版本固定不等于自动获得安全修复；维护时应先在隔离环境验证升级，再有意地更新版本或 digest。

## 备份

`scripts/backup.ps1` 会产生一个带时间戳的目录，默认位置是 `backups/`。其中包含：

- `mysql.sql`：一致性 MySQL 逻辑备份（含 routines/events）；
- `redis.rdb`：Redis Stack RDB 快照；
- `uploads.zip`：上传文件目录；
- `manifest.json`：文件 SHA-256 与备份范围。

Redis 的 `SAVE` 会短暂阻塞 Redis，生产中应安排在低峰期。脚本不会打印任何数据库或 Redis 密码。

```powershell
# Docker Desktop / docker 在 PATH 时
.\scripts\backup.ps1

# 只需要 MySQL 与上传文件
.\scripts\backup.ps1 -SkipRedis

# Docker Engine 仅在 WSL 时
.\scripts\backup.ps1 -UseWSL -WSLDistro Ubuntu-24.04

# 生产 Compose 文件
.\scripts\backup.ps1 -ComposeFile .\docker-compose.production.yml
```

备份目录包含用户数据，已被 Git 忽略。将其复制到加密、访问受控且与主机分离的存储；至少定期在隔离环境完成一次恢复演练。

## 恢复演练与事故恢复

恢复会覆盖 MySQL 与 Redis 数据，并将当前 `uploads/` 移到同级的 `uploads.pre-restore-<timestamp>` 以便人工回退。因此脚本要求两个显式确认开关。

1. 在隔离环境验证 `manifest.json` 的哈希，确认备份目录、Compose 项目和目标环境正确。
2. 停止所有 GopherAI API、worker 和其他写入者；必要时先生成目标环境的临时备份。
3. 执行恢复：

```powershell
.\scripts\restore.ps1 `
  -BackupDirectory .\backups\20260729T010203Z `
  -ApplicationStopped `
  -ConfirmRestore
```

4. 恢复后先执行 `go run ./cmd/migrate -command verify`，然后启动 API，并通过 `/readyz` 验证依赖健康。
5. 对知识库执行一次查询/索引状态检查。Redis RDB 与 MySQL 元数据同时恢复时，RAG 索引通常应一致；若备份时点不同，以 MySQL 元数据为准，重新提交失败或待处理索引任务。

若仅恢复数据库，加入 `-SkipRedis` 或 `-SkipUploads`。若 Docker 只在 WSL 中运行，同时传入 `-UseWSL -WSLDistro Ubuntu-24.04`。不要在仍有 API/worker 写入时执行恢复。
