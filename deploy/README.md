# 本地依赖服务

`docker-compose.yml` 只启动 MySQL 8.0 与 Redis 7，应用进程由 `make run-*` 在本机运行。

## 端口与镜像

| 服务 | 镜像 | 宿主端口 | 数据卷 | 关键参数 |
| :--- | :--- | :--- | :--- | :--- |
| MySQL | `mysql:8.0` | `3306`（本机覆盖为 `13306`） | `mysql_data` | `--character-set-server=utf8mb4 --collation-server=utf8mb4_0900_as_cs` |
| Redis | `redis:7` | `6379`（本机覆盖为 `16379`） | `redis_data` | `--appendonly yes --appendfsync everysec` |

MySQL 固定 8.0，不使用本机 MySQL 8.4（架构决策 GR-01）。初始化脚本 `mysql/init/00-grants.sql` 建 `yunshu` 与 `yunshu_log` 两个 schema，排序规则均为 `utf8mb4_0900_as_cs`。

## 凭据边界

`MYSQL_ROOT_PASSWORD` 缺省为 `root`，且 `MYSQL_ROOT_HOST=%` 允许宿主进程直连。这两项只服务于本地开发；生产环境的口令必须由密钥管理注入，不得复用此 compose 文件中的默认值。

## 起停

```bash
make up      # docker compose -f deploy/docker-compose.yml up -d --wait
make down    # 保留数据卷
docker compose -f deploy/docker-compose.yml ps
```

`make up` 会等待两个 healthcheck 通过；任一服务不健康时命令以非零码退出。

### 无 make 的机器

`scripts/dev.sh` 是 Makefile 的等价入口（本机未安装 GNU make，两条路径共存，行为一致）：

```bash
scripts/dev.sh up      # 自动探测端口占用，必要时套用覆盖文件
scripts/dev.sh env     # 按探测到的端口生成 .env
scripts/dev.sh check   # vet + test + 三条 grep 断言
scripts/dev.sh run-ctl # 载入 .env 后启动控制面
```

### 宿主端口已被占用时

若 3306 / 6379 已被本机 MySQL 8.4 或其他项目容器占用，套用覆盖文件把**宿主侧**端口换掉（容器内仍是 3306 / 6379，schema 与排序规则不受影响）：

```bash
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.portmap.yml up -d --wait
# 或
scripts/dev.sh up    # 探测到占用会自动加 -f deploy/docker-compose.portmap.yml
```

覆盖文件必须用 `ports: !override`：compose 对序列默认是**追加**，普通写法会同时发布 3306 而继续冲突。

换了宿主端口后 `.env` 里的 DSN 与 `REDIS_ADDR` 必须同步（`scripts/dev.sh env` 会自动改写）：

```
DB_DSN='root:root@tcp(127.0.0.1:13306)/yunshu?...'
REDIS_ADDR=127.0.0.1:16379
```

**不得**改为指向本机 MySQL 8.4（GR-01 锁 8.0）。

## 验证

```bash
# 服务端字符集与排序规则
docker exec -i yunshu-mysql mysql -uroot -proot -N \
  -e "SELECT @@collation_server, @@character_set_server"
# 期望：utf8mb4_0900_as_cs  utf8mb4

# schema 排序规则
docker exec -i yunshu-mysql mysql -uroot -proot -N \
  -e "SELECT schema_name, default_collation_name FROM information_schema.schemata \
      WHERE schema_name IN ('yunshu','yunshu_log')"

# Redis AOF
docker exec -i yunshu-redis redis-cli CONFIG GET appendonly appendfsync
# 期望：appendonly yes / appendfsync everysec
```

## 常见故障

| 现象 | 原因 | 处理 |
| :--- | :--- | :--- |
| `port is already allocated` / `bind: address already in use` | 3306 或 6379 被本机服务或其他容器占用 | 加 `-f deploy/docker-compose.portmap.yml` 换宿主端口（或 `scripts/dev.sh up` 自动处理）；**不得**把 DSN 改指本机 MySQL 8.4 |
| MySQL 健康检查一直 `starting` | 首次初始化需要 20–60s | 看 `docker logs yunshu-mysql`；`start_period` 已设为 40s |
| `Access denied for user 'root'` | `.env` 的 `MYSQL_ROOT_PASSWORD` 与卷内旧密码不一致 | 删除卷重建（见下）或改回旧密码 |
| 建库脚本未执行 | `mysql_data` 卷已存在，初始化脚本只在空卷首次启动时运行 | `docker compose -f deploy/docker-compose.yml down -v` 后重新 `make up` |
| Redis 重启后数据仍在 | AOF everysec 正常行为 | 不需要处理；Redis 全丢也不影响资金正确性 |

清空本地数据：

```bash
docker compose -f deploy/docker-compose.yml down -v
```

该命令删除 `mysql_data` 与 `redis_data`，不可恢复；只用于本地开发。
