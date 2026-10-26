# 本地开发环境（T01 基线）

目标：五个步骤跑通 `gatewayd` / `controld` / `jobsd` 骨架，并验证 `healthz` 与 `readyz` 的语义差异。

## 1. Clone 与工具链

```bash
git clone <repo-url> yunshu-api
cd yunshu-api
go version                 # 语言级别 >= 1.24；发布构建使用 >= 1.27.3
docker version             # 需要 Docker Engine 可用
docker compose version
```

`go.mod` 中的 `go 1.24.0` 是最低语言版本，不锁定 toolchain。`govulncheck` 在 go1.27.1 上报告 7 条标准库可达漏洞（GO-2026-6607 等，修复于 go1.27.3）；发布构建使用 go1.27.3 或对应版本的最新补丁，当前代码在 go1.27.3 下扫描结果为 0 条可达漏洞。

`make` 需要 GNU Make。Windows 仅在 Git Bash + GNU Make 下可用；没有 `make` 时执行下方“等价命令”列。

## 2. 启动依赖

```bash
cp .env.example .env
make up
docker compose -f deploy/docker-compose.yml ps
```

| 容器                           | 期望状态      | 端口     |
| :--------------------------- | :-------- | :----- |
| `yunshu-mysql` (`mysql:8.0`) | `healthy` | `3306` |
| `yunshu-redis` (`redis:7`)   | `healthy` | `6379` |

`make up` 失败时先看 `deploy/README.md` 的端口占用与健康检查两行。3306/6379 被其他项目占用时，停掉占用方；不要把 DSN 指向本机 MySQL 8.4。

## 3. 迁移

```bash
make migrate
# 输出：T01 has no migrations; schema migrations start in T02.
```

T01 只建空 schema（`yunshu`、`yunshu_log`），建表从 T02 开始。

## 4. 启动进程

三个终端分别执行：

```bash
make run-ctl      # 控制面 :8081
make run-gw       # 网关 :8080，metrics :9090
make run-jobs     # 后台任务，无对外端口
```

等价命令（Windows 无 make 时）：

```bash
set -a; . ./.env; set +a; go run ./cmd/controld
set -a; . ./.env; set +a; go run ./cmd/gatewayd
set -a; . ./.env; set +a; go run ./cmd/jobsd
```

期望日志为单行 JSON，含 `ts,level,svc,env,pid,trace_id,request_id,uid,kid,model,stage,latency_ms,status,msg` 全部键。

## 5. 探针验证

```bash
curl -s localhost:8081/healthz
# {"status":"ok"}

curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/readyz
# MySQL 与 Redis 均可用：200

docker compose -f deploy/docker-compose.yml stop redis
curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/healthz   # 200
curl -s localhost:8081/readyz; echo
# 503 {"checks":{"config":"ok","mysql":"ok","redis":"down"},"status":"not_ready"}
docker compose -f deploy/docker-compose.yml start redis
```

判定规则：

| 端点         | 探测依赖           | Redis 停止后的结果                   |
| :--------- | :------------- | :----------------------------- |
| `/healthz` | 否，只反映进程存活      | 仍 `200 {"status":"ok"}`        |
| `/readyz`  | Redis、MySQL、配置 | `503`，`checks.redis == "down"` |

`jobsd` 没有 HTTP 端口；通过日志确认任务表已注册，`Ctrl+C` 后应出现 `drain started` 与 `drain complete`。

## 环境变量

| 变量                      | 必填进程                  | 说明                                |
| :---------------------- | :-------------------- | :-------------------------------- |
| `ENV`                   | 三进程                   | `dev` / `staging` / `prod`，写入每条日志 |
| `SESSION_SECRET`        | `gatewayd`、`controld` | 会话签名密钥                            |
| `DB_DSN`                | `controld`、`jobsd`    | 主库读写 DSN                          |
| `LOG_SQL_DSN`           | 三进程                   | 日志库 DSN（同实例独立 schema）             |
| `GW_WRITE_DSN`          | `gatewayd`            | 网关唯一主库受限通道                        |
| `REDIS_ADDR`            | 三进程                   | `host:port`                       |
| `REDIS_PASSWORD`        | 可选                    | 空表示无密码                            |
| `GATEWAY_METRICS_ADDR`  | 可选                    | 默认 `:9090`                        |
| `GATEWAY_METRICS_TOKEN` | 可选                    | 非空时 `/metrics` 要求 Bearer          |
| `GATEWAY_DB_MAX_CONNS`  | 可选                    | 默认 `8`，必须 `>= 1`                  |

缺失必填变量时进程不会逐条报错：一次列出全部缺失项后 `exit 1`。

## 常用命令

```bash
go build ./... && go vet ./... && go test ./...
make check          # vet + test + encoding/json、context.Background、许可字符串断言
make down           # 停止依赖，保留数据卷
```
