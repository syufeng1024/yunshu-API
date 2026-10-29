# T36 · 备份 / 恢复 / DR 演练（T22 段 4，解挂 G6）

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T36 |
| 阶段 | 6 · 交付（运维件，对应 G6 解挂） |
| 优先级 | **P0**（Go/No-Go 门槛 4 与 7 的唯一依赖） |
| 依赖 | T22（原文要求）、T09（8 条对账断言实现） |
| 交付根 | `scripts/`、`docs/dr-plan.md`、`docs/tasks/delivery/` |
| 状态 | 🟨 部分通过（门槛 7 ✅；门槛 4 不能举证 → OI-14） |
| 派单方式 | 段 1 = 脚本 + DR 文档；段 2 = 真实演练 + 报告 |

## 1. 目标

补齐 T22 段 4 的运维件，让两件事可举证：

- **门槛 7**：备份与恢复脚本存在、可执行、有演练记录；
- **门槛 4**：全量恢复后 **8 条对账断言 0 差异**。

## 2. 现状

| 事实 | 说明 |
| :--- | :--- |
| `scripts/backup.sh`、`scripts/restore.sh`、`docs/dr-plan.md` **均不存在** | `open-issues.md` OI-09 已登记 |
| 8 条对账断言定义在 `docs/settlement.md` §2，由 `internal/recon/daily.go` 执行，违例检测用例在 `internal/recon/assert_test.go` | 演练要用**真实代码路径**，不许自己写 SQL 冒充 |
| 数据库：`yunshu`（60 表）、`yunshu_log`（日志库）、`yunshu_test`（测试库，**不备份**） | 迁移版本 0026 |
| 栈：`yunshu-stack-mysql` 容器，root 口令在 `deploy/.env.stack`（**不得出现在命令行参数里**） | 与 `seed-demo.sh` 同样的取值方式 |

## 3. 段 1 交付物

### 3.1 `scripts/backup.sh`

- 用 `docker exec` + `mysqldump --single-transaction --routines --triggers --set-gtid-purged=OFF` 导出 `yunshu` 与 `yunshu_log`（库名可覆盖）；
- 口令从 `deploy/.env.stack` 读取，**通过 `MYSQL_PWD` 环境变量传入容器**，不出现在 `ps` / 日志；
- 产物：`backups/yunshu-YYYYmmdd-HHMMSS.sql.gz` + `backups/yunshu-YYYYmmdd-HHMMSS.manifest`（含库名、迁移版本 `MAX(version)`、表数量、行数摘要、sha256）；
- 失败即非零退出并打印原因；`--keep N` 保留最近 N 份（默认 7）；
- `backups/` 加入 `.gitignore`。

### 3.2 `scripts/restore.sh`

- `bash scripts/restore.sh <dump.gz> [--db yunshu] [--yes]`；
- **默认只能恢复到非 `yunshu` 的库**（如 `yunshu_drill`），要覆盖 `yunshu` 必须显式 `--db yunshu --yes`，且打印警告并要求二次确认（`--yes` 也不跳过这条打印，但不再交互）；
- 恢复前校验 dump 的 sha256 与 manifest 一致；恢复到目标库后打印表数量与关键表行数（`t_user`/`t_ledger_entry`/`t_usage_record`/`t_recharge_order`）；
- 恢复后**不自动执行迁移**（避免静默改 schema），如目标库版本低于 dump 版本则报错退出。

### 3.3 `docs/dr-plan.md`

至少包含：RPO/RTO 目标值、备份策略（频率/保留/存放）、恢复步骤（含演练与真实故障两条路径）、8 条对账断言的验证方法、演练清单（checklist）、已知限制（单机 MySQL、无异地副本）。

## 4. 段 2 交付物

- 真实演练一次：**备份 → 恢复到 `yunshu_drill` → 跑 8 条对账断言 → 比对结果**；
- 产出 `docs/tasks/delivery/dr-drill-2026-10-29.md`：命令、耗时、行数比对、8 条断言逐条结果、结论；
- 更新 `go-live.md` 门槛 4 / 7 与 `open-issues.md` OI-09。

## 5. 硬约束

1. **口令不得进命令行**（`mysql -pXXX` 这类一律禁止）。
2. 不得对 `yunshu_test` 做任何备份/恢复操作；不得 DELETE/TRUNCATE 生产库数据。
3. 恢复脚本默认不碰 `yunshu`，必须有显式开关。
4. 8 条断言必须走 `internal/recon` 的真实实现（跑测试或调用其入口），**不得自写等价 SQL 冒充**。
5. `bash scripts/check-eol.sh` 退出码 0（新脚本必须是 LF）；不 commit / 不 push。

## 6. 验收口径

| # | 查什么 | 通过标准 |
| :--- | :--- | :--- |
| 1 | 备份 | `bash scripts/backup.sh` 产出 `.sql.gz` + `.manifest`，manifest 里迁移版本 == 0026，sha256 与文件一致 |
| 2 | 恢复 | 恢复到 `yunshu_drill` 成功，6 张关键表行数与源库一致 |
| 3 | 安全默认 | 不带 `--yes` 直接 `--db yunshu` 被拒；口令不出现在 `ps`/脚本输出里 |
| 4 | 幂等 | 连续两次备份、连续两次恢复到同一 scratch 库都成功（第二次覆盖） |
| 5 | 对账 | 8 条断言在恢复库上 0 差异（段 2） |
| 6 | 文档 | `dr-plan.md` 含 RPO/RTO、步骤、清单、限制；演练报告含命令与结果 |

## 7. 交付记录

### 段 1 · 脚本 + DR 文档

派单 14:26，交付 14:33（60 turns / 411s）。

| 项 | 结论 |
| :--- | :--- |
| `scripts/backup.sh` | 每库一个 gz（**不用 `--databases`**，否则 dump 里带 `CREATE DATABASE`，恢复到 `yunshu_drill` 会被重定向回 `yunshu`）+ `.manifest`（sha256、`schema_version=0026`、61 表、6 张关键表行数）；`--keep` 保留策略 |
| `scripts/restore.sh` | 默认只能恢复到 `yunshu_drill`；`--db yunshu` 必须 `--yes`；`*_test` **无开关可绕过**；恢复前校验 sha256 与 dump 头部无 `USE`；恢复后打行数比对，表数变少即退出 1 |
| 口令 | 比卡面更严：`MYSQL_PWD="$pw" docker exec -e MYSQL_PWD`（环境继承），宿主 `docker.exe` 与容器内 argv 都没有口令 |
| `docs/dr-plan.md` | RPO/RTO、备份策略、演练与真实故障两条恢复路径、8 条断言验证方法、15 项清单、13 项已知限制 |

### 段 2 · 真实演练

派单 14:34，交付 14:40（70 turns / 367s）。新增 `cmd/recondrill` + `scripts/recondrill.sh`（`ENV=local` 门禁、拒 `*_test`、只写 `t_reconcile_diff`/`t_notification`），**走生产对账同一入口**，而不是把测试 schema 指向演练库（否则 `TestMain` 会跑迁移、`daily_test` 会插 1,000 行，0 差异就不来自恢复数据了）。

| 步骤 | 结果 |
| :--- | :--- |
| 备份 | 退出 0，10.3s，manifest `0026`、sha256 一致 |
| 恢复 → `yunshu_drill` | 两次均退出 0（约 10.5s），61 表、6 张关键表行数逐表相等 |
| 8 条断言 | **R03 有 1 条 CRITICAL 差异**（两次运行可复现） |

**门槛 7（备份恢复演练）：✅ 转通过。**

**门槛 4（8 条对账断言 0 差异）：不能举证，保持 ❌。** 三个原因：

1. `docs/settlement.md` §2 的 8 条与 `internal/recon` 的 R01–R08 **不是一一对应**：4 条无实现（批次余额和、孤儿流水、未结算成功请求、配置版本可回溯）、1 条部分覆盖（金额守恒只有逐笔无日窗口）；反向还有 R02/R04/R06/R07/R08 在 §2 里没有独立行。
2. R03 实测 1 条差异：`id=33/34` 是当天实时流水，`id=35..42` 的 `created_at` 却在更早日期，**id 顺序 ≠ `created_at` 顺序**，按 id 升序比较余额连续性就会误报。R01 为 0、账户余额与 78 条流水合计一致，所以不是余额错，但按现行语义就是 1 条 CRITICAL。
3. R05 / `runExternal` 本次空跑（`t_user_daily_quota`、`t_upstream_bill` 均 0 行）。

补齐清单已写入 `dr-drill-2026-10-29.md` §5.3，登记为 **OI-14（阻塞）**。

### 自检

`go build ./...` + `go vet ./...` 通过；`go test ./internal/recon/... -count=1` → `ok 18.657s`；`bash scripts/check-eol.sh` → `EOL=0`。

### 遗留

| # | 项 | 处置 |
| :--- | :--- | :--- |
| 1 | 门槛 4 举证缺口（口径不对应 + R03 排序语义 + 空跑） | OI-14，需先裁定「以 §2 还是 R0x 为准」，再补检查 |
| 2 | R03 疑似语义缺陷（id 顺序 ≠ created_at 顺序时误报） | 建议单开卡：比较顺序改 `(created_at, id)`，或明确「流水必须按 id 单调写入」并加约束。**本轮只记录不改** |
| 3 | RPO ≤5min / RTO 745GB 稳态未达标 | `dr-plan.md` 已登记：无 binlog 归档、mysqldump 不适合大库，需 XtraBackup + 异地副本 |

