# T40-a · 网关写入 `t_usage_record.config_version`

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T40-a |
| 阶段 | 6 · 交付（门槛 4 举证） |
| 优先级 | **P0**（OI-16；门槛 4 第 8 条「种子数据假绿」的直接原因） |
| 依赖 | 无（`cfg:ver` 发布链路已由 T30 打通） |
| 交付根 | `internal/gw/`、`internal/dispatch/` |
| 状态 | ✅ 代码级验收通过（`f5d168d`；真机举证见 §8 遗留） |
| 分段 | **单段**（预计 ≤ 15 分钟；不做 段 2） |

## 1. 事实（已核，勿重复调查）

| 事实 | 证据 |
| :--- | :--- |
| `settle.UsageRecord` **有** `ConfigVersion` 字段 | `internal/settle/usagewriter.go:64` |
| 落库时**原样写**该字段，无兜底 | `internal/settle/usagewriter.go:479` |
| `dispatch` 构造 `UsageRecord` 时**没有赋**这个字段 → 恒为 0 | `internal/dispatch/usagerecord.go:117`（结构体字面量起点，字段列表里无 `ConfigVersion`） |
| R09 对 `config_version = 0` **豁免**，所以生产环境 R09 永远无样本 | `internal/recon/daily.go:747` 起 `checkR09`，`WHERE ... AND u.config_version > 0` |
| 演练库实测：43 条用量里 35 条 live 请求全为 `0`，8 条 `=7` 全是 `demo-seed-req-*` | `docs/tasks/delivery/dr-drill-2026-10-29.md` §7 |

**结论**：R09 目前的 `total_diffs=0` 只证明种子行可回溯，不证明生产可回溯。本卡是让 R09 拿到非种子样本的前置条件。

## 2. 要做（五条，按序）

1. **`gw.RoutingSnapshot` 增版本号**
   `internal/gw/catalog.go:44` 的 `RoutingSnapshot` 增 `Version int64`（`json:"-"`，它由 decode 赋值，不是快照成员）；`decodeRouting`（`internal/gw/catalog.go:466`）里 `rs.Version = snap.Version()`。

2. **`Catalog` 增无锁取版本**
   新增 `func (c *Catalog) Version() int64`：nil 安全；`static != nil` → `c.static.Version`；否则 `c.store.Snapshot(config.NsRouting)` 取 `Version()`，缺快照返回 0。
   **必须无锁、不 decode**（`store.Snapshot` 是原子读；`Current()` 会加锁+按指针缓存，不要在每请求入口再调它）。

3. **`gw.Ctx` 增字段并在两个入口盖章**
   `internal/gw/ctx.go` 的 `Ctx` 增 `ConfigVersion int64`（注释写明「本请求所用 ROUTING 快照的 `cfg:ver`」）。
   在 `internal/gw/server.go:84`（公开路径）与 `internal/gw/internal.go:83`（T27 内网调试台）创建 `Ctx` 后**各加一行** `c.ConfigVersion = h.d.Catalog.Version()`（`internal.go` 里是 `h.h.d`）。
   两处都要改：只改公开路径的话，调试台链路仍写 0。

4. **`dispatch` 写进 usage 行**
   `internal/dispatch/usagerecord.go:117` 的结构体字面量加 `ConfigVersion: c.ConfigVersion,`。

5. **补断言（两个层级，都要）**
   - `internal/dispatch` 单测：`gw.NewStaticCatalog(&gw.RoutingSnapshot{Version: 42, ...})` 跑通一次 `Serve`，断言 `UsageSink` 收到的 `record.ConfigVersion == 42`（**同时断言 `!= 0`**，否则 42 写成 0 也「等于」）。
   - `internal/gw` 单测：`Handler.ServeHTTP` 或 `InternalHandler.Serve` 入口后 `c.ConfigVersion > 0`（用包内既有 TestMain 已设的 `cfg:ver`；见 `internal/gw/testmain_test.go:191`）。

## 3. 硬约束

1. **禁止在 dispatch / gw 的请求路径上直接读 Redis 的 `cfg:ver`**：每请求一次 RTT，违反「路由与鉴权只读快照」的既有约束（`internal/gw/ctx.go` 包注释）。版本必须来自已加载的快照。
2. **不修改 `checkR09` 的 `config_version > 0` 豁免**，不改 `internal/recon/` 任何文件。0 仍是合法的历史语义。
3. **不改表结构**（`t_usage_record.config_version` 列已存在），不新增迁移。
4. **不动种子数据**：`demo-seed-req-*` 的 `config_version=7` 保留；R09 对 7 已能回溯。
5. 版本号取 **ROUTING 命名空间**的快照版本（它才是本次收费/路由所用的那一版）。不要取 `cfg:now`、不要取任意命名空间。
6. 不得在日志、错误信息、指标里打印 `upstream_key_enc`、正文等敏感值（沿用既有约束）。
7. `bash scripts/check-eol.sh` 退出码 0；`go vet ./...` 干净。
8. **不 commit、不 push、不建分支**。交付总监复核后提交。

## 4. 自检命令

本机无 Go、无 make，一律在容器里跑（Git Bash 需 `MSYS_NO_PATHCONV=1`，否则 `/src` 会被转成 Windows 路径）：

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v yunshu-gomod:/go/pkg/mod -v /d/codes/yunshu-api:/src -w /src \
  -e TEST_DB_DSN='root:root@tcp(host.docker.internal:13306)/yunshu_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true' \
  -e TEST_REDIS_ADDR='host.docker.internal:16379' \
  golang:1.27 go test ./internal/gw/ ./internal/dispatch/ ./internal/settle/ -count=1
```

（`yunshu-gomod` 是已预热的模块缓存卷，带上它可省掉每次 ~2 分钟的依赖下载。）

再跑一次编译与静态检查：

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v yunshu-gomod:/go/pkg/mod -v /d/codes/yunshu-api:/src -w /src golang:1.27 sh -c 'go build ./... && go vet ./...'
```

## 5. 验收口径（交付总监独立复跑，不采信转述）

| # | 查什么 | 通过标准 |
| :--- | :--- | :--- |
| 1 | 字段真的写进去了 | `internal/dispatch/usagerecord.go` 的结构体字面量含 `ConfigVersion:`，且值来自 `c.ConfigVersion`，不是常量 |
| 2 | 两个入口都盖章 | `server.go` 与 `internal.go` 都有赋值；**抽掉任一处，测试必须红** |
| 3 | 断言有效（反证） | 把 `ConfigVersion:` 改回不赋值/置 0，dispatch 用例必须红；恢复后绿 |
| 4 | 无热路径 Redis | `grep -rn "cfg:ver\|ConfigVersionKey" internal/gw internal/dispatch`（排除测试）无命中 |
| 5 | 回归 | `internal/gw`、`internal/dispatch`、`internal/settle` 全绿；`go build ./...`、`go vet ./...` 干净；`check-eol.sh` 退出 0 |

## 6. 不在本卡范围（写了会被打回）

- `t_user_daily_quota` / `t_upstream_bill` 的生产写入路径（T40-b / T40-c，需单独立项）。
- 错误码表收口（T39）。
- `internal/obs/log/record.go:72` 的 `ConfigVersion` **同样无生产者**——属请求日志（P30）缺口，本卡只要求回执里记一行，**不要顺手改**。
- 重跑 `recondrill`、改 `go-live.md` / `open-issues.md`：由交付总监在验收后做。

## 7. 完成回执（已归档）

**结果**：✅ 通过（代码级）。提交 `f5d168d`，8 文件 **+189 / -0**（纯增量，未动既有行）。

### 7.1 实现落点

| 文件:行 | 内容 |
| :--- | :--- |
| `internal/gw/catalog.go:48` | `RoutingSnapshot.Version int64`（`json:"-"`，由 decode 盖章，非 payload 成员） |
| `internal/gw/catalog.go:257` | `func (c *Catalog) Version() int64`：nil 安全 → `static.Version` → `store.Snapshot(NsRouting).Version()` → 0；**无锁、不 decode** |
| `internal/gw/catalog.go:497` | `decodeRouting` 中 `rs.Version = snap.Version()` |
| `internal/gw/ctx.go:91` | `Ctx.ConfigVersion int64` |
| `internal/gw/server.go:91` | 公开入口盖章 `h.d.Catalog.Version()` |
| `internal/gw/internal.go:90` | T27 调试台入口盖章 `h.h.d.Catalog.Version()` |
| `internal/dispatch/usagerecord.go:119` | `settle.UsageRecord{ConfigVersion: c.ConfigVersion}` |

### 7.2 新增用例

| 用例 | 位置 | 断言 |
| :--- | :--- | :--- |
| `TestHandlerStampsRoutingConfigVersion` | `internal/gw/cfgversion_test.go:64` | 走真实 `writeRouting` loader 发布快照后，`Ctx.ConfigVersion > 0` 且 `== store 版本` |
| `TestInternalHandlerStampsRoutingConfigVersion` | `internal/gw/cfgversion_test.go:88` | 同上，T27 入口 |
| `TestP28UsageRecordCarriesConfigVersion` | `internal/dispatch/settle_test.go:119` | static catalog `Version: 42` → usage 行 `ConfigVersion == 42` 且 `!= 0` |

### 7.3 交付总监独立复跑（不采信转述）

| 命令 | 结果 |
| :--- | :--- |
| `go test ./internal/gw/ ./internal/dispatch/ ./internal/settle/ -count=1` | `ok gw 0.517s` / `ok dispatch 0.577s` / `ok settle 1.972s`，exit 0 |
| `go build ./... && go vet ./...` | exit 0 |
| `go test ./internal/... -count=1 -p 2` | exit 0（全 internal 无 FAIL） |
| `bash scripts/check-eol.sh` | exit 0 |

### 7.4 反证（交付总监亲自做，逐条恢复后复绿）

| 改坏点 | 期望红 | 实测 |
| :--- | :--- | :--- |
| 删 `usagerecord.go:119` 的 `ConfigVersion: c.ConfigVersion` | dispatch 用例红 | ✅ `usage config_version = 0, want 42 and non-zero` |
| 删 `server.go:91` 盖章 | 公开入口用例红、internal 用例**仍绿** | ✅ `Ctx.ConfigVersion = 0, want > 0`；internal PASS |
| 删 `internal.go:90` 盖章 | internal 用例红、公开用例**仍绿** | ✅ 同上，反向成立 |

两处入口各自被独立覆盖，不存在「改一处两处一起绿」的假覆盖。还原后 `git diff` 逐文件仍为 +1/-0。

### 7.5 硬约束抽样

| 约束 | 证据 |
| :--- | :--- |
| 请求路径不直读 Redis `cfg:ver` | `grep -rn "cfg:ver\|ConfigVersionKey\|RedisVersionKey" internal/gw internal/dispatch`（排除 `_test.go`）仅命中 4 行注释 |
| 未碰 `internal/recon` / 迁移 / `internal/obs` | `git status --short` 无命中 |
| 无锁 | `Store.Snapshot` = `s.snaps[i].Load()`（`internal/config/snapshot.go:206`，`atomic.Pointer`） |
| 未 commit / 未建分支（实现方） | 由交付总监复核后提交 |

### 7.6 偏离（已确认，不构成打回）

1. dispatch 用例直接调 `Dispatcher.Serve`（绕过 `gw.Handler`），Ctx 版本由 `d.opts.Catalog.Version()` 显式赋值；**入口盖章链路由 gw 的两条用例独立举证**，两段合起来覆盖「入口 → P28」全链。若把两段拆开看，任何一段单独删掉都会红。
2. 新增注释用英文，与 `internal/gw` 包内既有注释语言一致。

## 8. 遗留（本卡未做，需单独立项或另派）

1. **真机举证未做**：门槛 4 / OI-16 的原始验收口径是「重跑 `recondrill` 后 R09 仍 0 差异，且 `t_usage_record` 里**新请求** `config_version > 0`」。当前栈跑的是旧 `gatewayd` 镜像，需重建镜像 → 重启 `gatewayd` → 打真实请求 → `bash scripts/recondrill.sh` 复跑，才能把 OI-16 从「代码已修」改成「已举证」。**在此之前 OI-16 不得关闭，门槛 4 仍为 ❌。**
2. `internal/obs/log/record.go:72` 的 `ConfigVersion` 同样无生产者（P30 请求日志缺口），本卡按要求未改，需另记 BACKLOG。
3. `t_user_daily_quota` / `t_upstream_bill` 生产者缺口（OI-15）未动，属 T40-b / T40-c。

