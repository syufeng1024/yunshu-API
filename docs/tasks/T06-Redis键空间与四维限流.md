# T06 · Redis 键空间与四维限流

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T06 |
| 阶段 | 2 · 核心域 |
| 优先级 | P0（对外承诺 G-10/G-11 的载体） |
| 依赖 | T03 |
| 可并行 | 与 T04 / T05 / T11 并行 |
| 交付根 | `internal/redisx/`、`internal/ratelimit/`、`internal/idemstore/`、`internal/breaker/` |
| 状态 | ⬜ 未开始 |

## 1. 目标

按 TDS §2 落地 26 个 Redis 键与原子边界：四维限流（一次 Lua 判定）、并发租约、TPM 占位/结算、日额度、幂等快路径、余额镜像、熔断状态、日志 Stream。**Redis 全丢不影响资金正确性**这一前提必须在本卡的代码注释里写死（GR-02）。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| TDS §2 前言 | 2957–2961 | **Redis 5.0.9 能力边界**；`maxmemory-policy noeviction`；内存估算 |
| TDS §2.1 | 2965–3023 | K1–K26 键名/类型/TTL/写入时机/原子性要求；**TPM 占位口径五条**；K13 写穿、`grp/gp`、K19 只存标准价 |
| TDS §2.2 L1 | 3029–3073 | `rl_acquire.lua` 的 KEYS/ARGV/判定次序/返回 8 下标/失败分支/时钟漂移 |
| TDS §2.2 L2 | 3075–3094 | `rl_release.lua` 的释放与补偿、跨分钟不补 TPM |
| TDS §2.3 | 3206–3223 | 限流维度降级矩阵（并发降、其余 fail-close） |
| TDS AD-04 / AD-09 | 110、115 | 网关无状态、限流状态全在 Redis；并发降级后 per-Key 上限 = 名义值 ÷ 副本数 |
| TDS 第一篇 §1 | 169–210 | `bal:{userID}` 镜像规格：微积分、60s TTL、**DEL 不 SET**、缺失回源再缺失 503 |
| TDS §1.6 | 2419–2610 | 幂等租约 15s / 续约 5s / fence_token |
| PRD §3.2 | 243–279 | 四维定义、优先级、日额度按账户、TPM 口径、免费档 3 Key |
| PRD §3.3 | 326–362 | 429 契约字段；多维触顶取**恢复最晚**者 |

## 3. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `internal/redisx/client.go` | 连接池、超时（单命令 50ms / Lua 100ms）、`maxmemory-policy` 启动自检（非 `noeviction` 即启动失败） |
| 2 | `internal/redisx/keys.go` | 26 个键的唯一构造函数（`RPMLKey(kid, minEpoch)` 等），禁止拼接字符串外泄 |
| 3 | `internal/redisx/clock.go` | `min_epoch` 计算：本地 `now_ms + redis_offset_ms`；每分钟 `TIME` 同步；偏移 >±1s WARN、>±5s ERROR 并暴露指标 |
| 4 | `internal/ratelimit/acquire.go` | L1 调用封装，返回 `Verdict{Allowed, Code, Limit, Remaining, ResetAt, AllHit[], ResetAtMax}` |
| 5 | `internal/ratelimit/release.go` | L2 封装；所有退出路径（成功/失败/断连/panic defer）统一调用，**幂等** |
| 6 | `internal/ratelimit/tpm.go` | 占位 → 结算差额回补；跨分钟不结转；无 usage 兜底（已输出 + 预估输入） |
| 7 | `internal/ratelimit/dailyquota.go` | 日额度累计（微积分）、按 `Asia/Shanghai` 自然日、TTL 到次日 00:10 + 600s |
| 8 | `internal/ratelimit/degrade.go` | 并发维度降级：节点内 `atomic.Int64` + `node:set` ZCARD 求副本数；`perKeyLocal = quota / replicas` |
| 9 | `internal/ratelimit/scripts/rl_acquire.lua` | L1 原文，**逐行对齐 TDS §2.2** |
| 10 | `internal/ratelimit/scripts/rl_release.lua` | L2 原文 |
| 11 | `internal/idemstore/idem.go` | K6/K7：`SET NX PX`、状态 `IN_FLIGHT`/`DONE`、租约 15s + 5s 续约、`fence_token`；**先写 DB 再写 Redis** |
| 12 | `internal/idemstore/idem.go` | 命中语义分流：非流式静默 200 + 缓存结果；流式 409（无法重放）；同键不同 `body_hash` → `idempotency_key_reused` |
| 13 | `internal/breaker/breaker.go` | K16 状态机（CLOSED/OPEN/HALF_OPEN）+ K17 半开探测租约（**探测结束主动 DEL**）+ K23 429 冷却（≤60s，不参与 `fc`） |
| 14 | `internal/balance/mirror.go` | K26 读写：`Get` 缺失→回源→再缺失 503；`Invalidate` 只用 `DEL` |
| 15 | `internal/logq/producer.go` | K24 `XADD`（`MAXLEN ~ 200000`、字段 `v`/`p`），`XAUTOCLAIM` **禁用**，接管用 `XPENDING` + `XCLAIM` |
| 16 | `internal/lock/lock.go` | K21/K22/K25：`SET NX` 对账锁、批次锁、jobsd 心跳 |
| 17 | `internal/ratelimit/*_test.go` | 用 `miniredis` 或真实 Redis：四维各自触顶、多维同时触顶、跨分钟补偿、租约清理、降级计数、偏移 |
| 18 | `docs/redis-keys.md` | 26 键 → 代码构造函数 → 写入方 → TTL → 失效条件（运维手册用） |

## 4. 硬约束（违反即驳回）

1. **命令能力边界**：只允许 5.0.9 可用命令。**禁用** `ZADD GT/LT`、`XAUTOCLAIM`、`FUNCTION`、`GETDEL`、`GETEX`、`COPY`。可用 `EVAL/PEXPIRE/SET NX PX/ZADD/ZREMRANGEBYSCORE/ZCOUNT/TIME/PUBLISH/HGET/HSET/INCRBY/XADD/XREADGROUP/XACK/XPENDING/XCLAIM`。
2. **`maxmemory-policy` 必须 `noeviction`**：启动自检，不符即退出。理由：限流与预扣键被驱逐会造成额度穿透与资金悬挂。
3. **L1 判定次序固定**：清理租约 → 读四维 → 并发 > RPM > TPM > 日额度 → 写入（`ZADD` 先，然后 K1、K2、K4）。**不得调换**：次序决定并发名额不会在写入后被后续维度拒绝而泄漏。
4. **L1 返回 8 个下标，不得增减**：`[1]allowed [2]code [3]limit [4]total [5]remaining [6]reset_at_sec [7]all_hit [8]reset_at_max`。
   **`[3]`–`[6]` 必须描述「对外报码的那个维度」（即恢复最晚者），而不是优先级最高的那个。**
   理由：对外 `code` 取恢复最晚者（硬约束 5），若 `limit/remaining/reset_at` 仍描述并发维度（+3s），客户端会看到「`daily_quota_exceeded` + 3 秒后重试」，重试必然再被拒——这正是 PRD §3.3 选「恢复最晚」要避免的。四维全满时：`[2]code = concurrency_limit_exceeded`（优先级）、`[3]`–`[6]` = 日额度的 limit/total/remaining/reset、`[8]reset_at_max` = 次日 00:00（UTC+8）。
5. **判定用优先级，报码用恢复最晚**：L1 只负责给出 `all_hit` 与 `reset_at_max`；对外 `code` 由 T10 按「日额度 > RPM/TPM > 并发」选取。**不得**在 Lua 里直接返回优先级最高的那个码。
6. **`all_hit` 必须是全部触顶维度**（逗号拼接），供 `message` 写「同时触顶：并发、RPM」；`limit/remaining/reset_at` 只描述主维度。
7. **fail-close**：Lua 执行失败（连接失败 / SCRIPT LOAD 也失败）→ 503 `rate_limiter_unavailable`，**不得放行**。`ratelimit.degrade.mode=concurrency_only` 仅降级**并发维度**，K1/K2/K4 仍 fail-close。
8. **TPM 五条**：① 计量 = 输入 + 输出；② 占位 = 输入 token + `max_tokens`（缺省取模型上限）；③ 结算按 `usage` **覆盖**（差额回补同一分钟桶）；④ 跨分钟不结转、目标桶过期则丢弃差额；⑤ 无 usage 时按「已输出 + 预估输入」结算，**不得整体释放占位**。
9. **日额度 TTL** = 距次日 **00:00**（UTC+8）的秒数 **+ 600s**，即键在 **00:10** 过期（TDS §2.2 L2 的「仍有 10 分钟窗口接受补偿」即指此）。`{yyyymmdd_cst}` 必须按 `Asia/Shanghai` 格式化，**不得**用 UTC 日期。
   **注意**：原卡写「到次日 00:10 的秒数 + 600s」是重复计了一次 600s，会得到 00:20（见 `errata.md` E-11）。
10. **`min_epoch` 用 `now_ms + redis_offset_ms`**，不得直接用本地时钟。偏移超阈值必须打日志并暴露指标（两副本落不同分钟桶会直接放大限额）。
11. **K26 只能用 `DEL` 失效，绝不允许 `SET` 新值**。代码审查项：`grep -rn "bal:" internal/ | grep -i "set"` 必须为空。
12. **K26 缺失处理**：回源主库一次 → 仍缺失返回 503，**不得按 0 处理**（按 0 误拒有余额用户）。
13. **K13 由控制面写穿、不设 TTL**；本卡只提供 `KeyAuthWriter` 接口（`Upsert` / `Delete` / `BulkUpdateGroupRatio`），不实现控制面调用。缺失即 401，**无主库回落**。
14. **K19 只存标准价**，不乘倍率；取价 = `K19 × K13.gp ÷ 10000`。
15. **K17 半开探测必须主动 `DEL`**：只靠 30s TTL 会让 3 次成功探测最坏 90s 才凑齐。
16. **K23 与熔断分离**：429 冷却不参与 `fc` 计数、不触发跳闸。
17. **幂等定序**：先写 DB（`t_idempotency_record`）再写 Redis；Redis 写失败只降级性能，**不得**让请求失败。读路径按 `(scope, api_key_id, idem_key)` + `body_hash`。
18. **流式幂等命中返回 409**（无法重放），非流式返回 200 + 缓存体；同键不同 `body_hash` 返回 `idempotency_key_reused`。
19. **所有计数器键名小写**，分隔符 `:`，键构造只允许在 `redisx/keys.go`。
20. **禁止在 Lua 里做时间相关的「跨桶」逻辑**：`{min_epoch}` 由调用方传入（`ARGV`），不在 Lua 内取 `TIME`。

## 5. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `go test ./internal/ratelimit/... ./internal/idemstore/... ./internal/breaker/... -race` 全绿 | 复跑 |
| A2 | 四维逐一触顶：RPM / TPM / 并发 / 日额度各返回正确 `code` 与 `remaining` | 表驱动单测 |
| A3 | **多维同时触顶**：四维全满 → `all_hit` 含 4 个码、`reset_at_max` == 次日 00:00（UTC+8），而 `[2]code` 仍是并发（优先级），证明"判定与报码分离" | 单测 |
| A4 | 并发租约：同 `request_id` 重复 `ZREM` 无副作用；过期成员被 `ZREMRANGEBYSCORE` 清掉 | 单测 |
| A5 | TPM 占位/结算：占位 1000、实际 800 → 桶值 800（差额 −200 回补同一桶）；跨分钟 → 不补偿且日志 debug | 单测 |
| A6 | 无 usage 兜底：不释放占位，按「已输出 + 预估输入」计 | 单测 |
| A7 | fail-close：关闭 Redis → 请求 503 `rate_limiter_unavailable`（不是 200） | 手工 |
| A8 | 降级：`degrade.mode=concurrency_only` 且 3 副本 → `perKeyLocal == ceil(quota/3)`，K1/K2 仍走 Redis | 单测 |
| A9 | K26：缺失回源后仍缺失 → 503；失效调用只发 `DEL` | 单测（抓命令） |
| A10 | 幂等：同键同体并发两请求 → 一个 `IN_FLIGHT` 另一个等租约；流式命中 → 409 | 单测 |
| A11 | 熔断：连续失败达阈值 → `OPEN`；`open_until` 到期 → `HALF_OPEN`；探测成功 `DEL` 租约 | 单测 |
| A12 | 日额度跨日：23:59 的扣减计入当日键，次日 00:01 落到新键 | 单测（假时钟） |
| A13 | 禁用命令扫描：`grep -rnE "XAUTOCLAIM|ZADD.*GT|FUNCTION|GETDEL|GETEX" internal/` 为空 | grep |
| A14 | 内存压测：50,000 Key × 2 分钟键，`INFO memory` < 300 MB | 手工 |
| A15 | `docs/redis-keys.md` 26 键齐备 | 人工 |

## 6. 自检命令

```bash
go test ./internal/ratelimit/... ./internal/idemstore/... ./internal/breaker/... ./internal/balance/... -race -cover
grep -rnE "XAUTOCLAIM|ZADD[^\n]*\bGT\b|FUNCTION |GETDEL|GETEX" internal/ || echo "OK: 无 6.2+ 命令"
grep -rn '"bal:' internal/ | grep -iE "SET " || echo "OK: K26 只 DEL"
docker exec -i $(docker compose -f deploy/docker-compose.yml ps -q redis) redis-cli CONFIG GET maxmemory-policy
```

## 7. 禁止事项

- 不得把限流状态放进程内存（除降落级的并发计数）。
- 不得用 Redis 做账本真相源（GR-02）：预扣/结算金额只认 `t_ledger_entry`。
- 不得给 K13 加 TTL（会让全部 Key 集体失效且无回落点）。
- 不得为「省一次往返」把 K1/K2/K4 合并成一个键（四维 TTL 与重置时刻不同）。
- 不得在 Lua 里读配置（限值与开关全部由 `ARGV` 传入），否则配置变更要重载脚本。

## 8. 完成回执（已归档 · 2026-10-26）

> 归档说明：本卡含**两轮补正**（TTL 基准、8 下标维度归属、平手次序），补正后通过。

| 项 | 内容 |
| :--- | :--- |
| 卡号 | T06 |
| 完成状态 | 完成（18 项交付物落地） |
| 验收结论 | ✅ 通过（经两轮补正） |
| 复核人 | 交付总监 |

**交付物**：`internal/redisx/{client,keys,clock,authcache}.go`、`internal/ratelimit/{acquire,release,tpm,dailyquota,degrade}.go` + `scripts/{rl_acquire,rl_release}.lua`、`internal/idemstore/`、`internal/breaker/`、`internal/balance/mirror.go`、`internal/logq/producer.go`、`internal/lock/`、`docs/redis-keys.md`。

**自检结果**

```
26 键                    K1–K26 构造函数齐备（另有 1 个 IdemHash 辅助）
禁用命令                  无 XAUTOCLAIM / FUNCTION / GETDEL / GETEX；ZADD 无 GT/LT/NX
maxmemory-policy         noeviction（启动自检）
A3 四维全满               [2]code=concurrency（优先级） outward=daily_quota
                         slots=1000/700/300 reset_at=次日 00:00（UTC+8）
A7 Redis 不可用           allowed=false, err=rate_limiter_unavailable（fail-close）
A8 并发降级               perKeyLocal=4=ceil(10/3)，K1/K2 仍走 Redis，K3 不建
TPM 五条                 占位 1000→usage 800 桶值 800；跨分钟不补偿；桶过期不重建；
                         无 usage 按「已输出+预估输入」=300，不整体释放
日额度 TTL               23:00 → 00:10 过期（1h10m）
K26                      只有 DEL，无 SET
时钟 offset              TIME 采样扣往返；>±1s WARN、>±5s ERROR
覆盖率                   ratelimit 68.5% / idemstore 77.4% / breaker 75.3% /
                         balance 82.8% / redisx 77.1% / logq 84.0% / lock 91.7%
-race（容器）             七个包全部 PASS
```

**两轮补正记录**

| 轮次 | 问题 | 修正 |
| :--- | :--- | :--- |
| 1 | 日额度 TTL 多算 10 分钟（00:20 而非 00:10） | 改基准为「距次日 00:00 + 600s」（E-11），新增用例断言 23:00 → 00:10 |
| 1 | `[3]`–`[6]` 描述了优先级首个维度（并发，+3s），而对外码是日额度 → 客户端会 3 秒后重试并再被拒 | 改为描述**恢复最晚维度**（E-13）；A3 断言 slots=1000/700/300 |
| 2 | 平手次序不一致：Lua 取先评估者（并发），Go `OutwardCode` 取日额度 → 23:59 那一分钟与每分钟最后 3 秒错配 | Lua 引入 rank（日额度4 > RPM3 > TPM2 > 并发1），新增两条平手用例 |

**偏离与遗留**

| # | 项 | 处置 |
| :--- | :--- | :--- |
| 1 | L1 的 ARGV 扩到 14 个（原 10 个） | 用于传 `min_epoch` / 日额度重置秒 / 降级开关 / TTL，8 下标返回不变 |
| 2 | 幂等 DB 适配器**未落 SQL**：`t_idempotency_record` 缺 `fence_token` / `lease_expires_at` 列，本卡禁止加迁移 | **T07 开工前必须决定**：加一条 Expand 迁移（0020）补两列，或改用 `request_id` 作 fence + `created_at+15s` 作租约锚点 |
| 3 | 降级模式 `ratelimit.degrade.mode` 不是 45 键之一（E-12） | 现由构造参数 `ratelimit.Mode` 注入，T07 从启动参数读取 |
| 4 | 限流与配置测试共用 Redis DB 15 | 全量跑需 `-p 1`；CI 里应串行或分库 |
| 5 | 指标 `dim` 标签取 `[2]` 而非对外维度 | 四维全满时 `gw_ratelimit_rejected_total{dim="concurrency"}` 与响应体 `daily_quota_exceeded` 不一致；**T10/T22 裁定取哪个** |
