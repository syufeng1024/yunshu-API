# 备份与灾难恢复计划

适用部署形态：`deploy/docker-compose.stack.yml` 单机栈，MySQL 8.0 运行在容器 `yunshu-stack-mysql`，库为 `yunshu`（61 张表，迁移版本 `0026`）与 `yunshu_log`（当前 0 张表）。执行脚本：`scripts/backup.sh`、`scripts/restore.sh`。本文件覆盖备份、两条恢复路径（演练 / 真实故障）与恢复后的 8 条对账断言验证方法。

## 1 RPO / RTO

目标值来自 PRD §9.9 / F-9：**RPO ≤ 5 分钟、RTO ≤ 1 小时**。当前仓库只有每日全量逻辑备份，达标的路径与差距如下。

| 指标 | 目标 | 段 1 交付后的实际值 | 差距与成立条件 |
| :--- | :--- | :--- | :--- |
| RPO | ≤ 5 分钟 | **等于备份间隔**。按每日 1 次执行时为最长 24 小时 | 未实现 binlog 归档。MySQL 已开 `log_bin=ON`、`binlog_format=ROW`、`binlog_expire_logs_seconds=2592000`（30 天），但没有任何归档任务；补齐每分钟归档并按 TDS §8.1.2 的算式，RPO ≈ 60s 归档周期 + 3s 传输 ≈ 63s |
| RTO | ≤ 1 小时 | 本次实测：41 KB dump、61 张表，恢复到 `yunshu_drill` 用时 **10s**（含 sha256 校验与导入，见 §7 命令） | 该数字只在当前数据量下成立。按 TDS §8.1.3 的稳态规模（约 745 GB），`mysqldump` 恢复需 6–10 小时，**必须换 XtraBackup 8.0** 才能守住 1 小时 |
| 备份频率 | 每日全量 + 每 5 分钟增量 | 仅每日全量（需外部调度，脚本不自带定时器） | 增量（binlog 归档）未实现 |
| 异地副本 | 异地与跨可用区各 1 份 | 无，产物只在宿主 `backups/` | 宿主磁盘损坏即同时丢失库与备份 |

**结论口径**：段 1 交付的是「可执行、可校验、可举证」的备份与恢复工具链；它**不构成 RPO ≤ 5 分钟 / RTO ≤ 1 小时的达标证据**，这两条仍挂在 `docs/tasks/delivery/open-issues.md` OI-09。

## 2 备份策略

| 备份项 | 当前实现 | 频率 | 保留 | 存放 | 加密 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| 全量·主库 `yunshu` | `mysqldump --single-transaction --quick --routines --triggers --set-gtid-purged=OFF --no-tablespaces --hex-blob` → `gzip` | 需外部调度，建议每日 02:00（UTC+8，避开 03:00 日对账） | `--keep N`，默认 7 份 | `<repo>/backups/`（可用 `--dir` 改） | 无。仅靠 `umask 077` 落成 0600 文件 |
| 全量·日志库 `yunshu_log` | 同上，独立文件 | 同上 | 随主库 | 同上 | 同上 |
| 增量 | 未实现 | — | — | — | — |
| 配置快照 | `t_config_item` / `t_config_publish` 在主库 dump 内，无需单独导出 | 随全量 | 随全量 | 同上 | 同上 |
| 密钥 / KEK | **不在备份内** | — | — | `deploy/.env.stack` / 密钥管理系统 | 由外部托管 |

产物与清单：

| 文件 | 内容 |
| :--- | :--- |
| `backups/yunshu-YYYYmmdd-HHMMSS.sql.gz` | 主库逻辑 dump（无 `CREATE DATABASE` / `USE`，可直接导入任意目标库） |
| `backups/yunshu_log-YYYYmmdd-HHMMSS.sql.gz` | 日志库 dump；当前该库 0 表，文件 471 B |
| `backups/yunshu-YYYYmmdd-HHMMSS.manifest` | `backup_id`、UTC/本地时间、`git_commit`、MySQL 版本、库列表；每个库一段 `[db ...]`，含 `file`、`bytes`、`sha256`、`schema_version`、`table_count`、6 张关键表行数 |

时间戳统一取 **UTC**（文件名与 `created_at` 一致，`created_at_local` 另记本地时间）。`sha256` 由 `sha256sum` 计算，`restore.sh` 导入前必须比对，不一致即拒绝。

调度示例（脚本自身不含定时器；下列命令里没有口令，脚本从 `deploy/.env.stack` 读取）：

```bash
# Linux cron：每日 02:00
0 2 * * * cd /srv/yunshu-api && bash scripts/backup.sh >> /var/log/yunshu-backup.log 2>&1
```

Windows 用任务计划程序调用同一命令；失败时退出码非 0，调度器需按非 0 退出码告警，否则备份中断不会有人发现。

## 3 恢复步骤

### 3.1 演练路径（恢复到 scratch 库）

演练不碰 `yunshu`，目标库固定 `yunshu_drill`（脚本默认值）。`yunshu_drill` 不存在时脚本自动 `CREATE DATABASE`（utf8mb4 / utf8mb4_0900_as_cs）。

```bash
# 1. 备份
bash scripts/backup.sh

# 2. 恢复（默认目标 yunshu_drill；sha256 自动校验）
bash scripts/restore.sh backups/yunshu-<ts>.sql.gz

# 3. 再跑一次，验证幂等（dump 内每张表是 DROP TABLE IF EXISTS + CREATE TABLE）
bash scripts/restore.sh backups/yunshu-<ts>.sql.gz

# 4. 行数比对（源库 / 演练库应一致）
docker exec -e MYSQL_PWD="$PW" yunshu-stack-mysql mysql -uroot -N -B -e "
  SELECT 't_user',          (SELECT COUNT(*) FROM yunshu.t_user),          (SELECT COUNT(*) FROM yunshu_drill.t_user)
  UNION ALL SELECT 't_ledger_entry',   (SELECT COUNT(*) FROM yunshu.t_ledger_entry),   (SELECT COUNT(*) FROM yunshu_drill.t_ledger_entry)
  UNION ALL SELECT 't_usage_record',   (SELECT COUNT(*) FROM yunshu.t_usage_record),   (SELECT COUNT(*) FROM yunshu_drill.t_usage_record);"
```

`$PW` 的取法同 `scripts/seed-demo.sh` 的 `env_get`：`awk -F= '/^MYSQL_ROOT_PASSWORD=/{...}' deploy/.env.stack`。手工执行时不要把口令写进 `-p` 参数。

脚本结束时的判定：

| 输出 | 含义 | 处理 |
| :--- | :--- | :--- |
| `sha256 校验通过` | dump 与 manifest 一致 | 继续 |
| 6 张关键表全 `OK`，`[restore] OK` | 恢复行数不低于 manifest 基线 | 通过 |
| 某表 `LOST` | 恢复行数少于 manifest | 退出码 1，重跑导入；仍失败则查导入错误 |
| 某表 `NEW（导出后源库有新写入）` | 恢复行数多于基线（备份后源库有写入） | 正常现象，不判失败 |
| `表数量 < manifest table_count` | 有表没导入成功 | 退出码 1 |

### 3.2 真实故障路径（覆盖 `yunshu`）

覆盖生产库需要 `--db yunshu --yes`。**`--yes` 只关闭交互，不关闭警告**；脚本无论如何都会打印风险块。

| # | 步骤 | 命令 / 判据 | 失败处理 |
| :--- | :--- | :--- | :--- |
| 1 | 冻结写入 | `docker stop yunshu-stack-gatewayd yunshu-stack-controld yunshu-stack-jobsd`；确认无新写入 | 有未完成的长请求时等待其超时再停 |
| 2 | 保留现场 | `bash scripts/backup.sh`，记录新 manifest 路径 | 备份失败也继续：说明现场已不可读，直接走第 4 步并保留容器文件系统 |
| 3 | 确认代码版本 | `git rev-parse --short HEAD` 与目标 dump 的 `git_commit` 对比；代码迁移版本不得高于 dump 的 `schema_version` | 代码更新时先用 `scripts/rollback.sh` 回滚镜像，再恢复 |
| 4 | 恢复 | `bash scripts/restore.sh backups/<dump>.sql.gz --db yunshu --yes` | 导入中断时库处于部分恢复状态：不要起服务，确认磁盘/连接后重跑同一命令覆盖 |
| 5 | 结构与行数 | 61 张表、`schema_version=0026`、6 张关键表行数 ≥ manifest | 少表/少行按失败处理，回到第 2 步的现场 dump |
| 6 | 对账断言 | 见 §4；R-01..R-08 在恢复库上 0 差异 | 有差异按 `docs/ops-runbook.md` §3 处置，不直接改余额 |
| 7 | 起服务 | 顺序：`controld` → `gatewayd` → `jobsd`；`controld` 启动时会重发 ROUTING / K19 快照 | `/readyz` 不 200 时看 `docker logs`，不要反复重启 |
| 8 | 冒烟 | `bash scripts/smoke.sh` 全 PASS（允许 WARN） | FAIL 项按输出定位 |
| 9 | 回滚 | 恢复后判定失败时，用第 2 步的现场 dump 再执行一次第 4 步 | 现场 dump 也损坏时，只能依赖更早的备份 + binlog 手工前滚 |

恢复后服务重启的三处缓存不在备份内，必须知道：

| 状态 | 恢复后表现 | 处理 |
| :--- | :--- | :--- |
| Redis ROUTING / K19 快照 | 与当前库内容可能不一致 | `controld` 启动同步 tick 重发；必要时 `docker restart yunshu-stack-controld` |
| 限流计数 K01–K15 | 归零 | 可接受：限流是保护性限值，归零只放宽不收紧 |
| 预扣 `t_prehold_hold` | 恢复时点仍 `HELD` 的行保留在库中 | 由 R-06（悬挂预扣扫描）与 `recon.Hanging` 发现；不要在恢复后手工删除 |
| 幂等键 `t_idempotency_record` | 在备份内，随库恢复 | 无额外处理 |

**密钥前置条件**：备份不包含 `CHANNEL_KEY_KEK` / `LOG_BODY_KEK` / `SESSION_SECRET` / `INTERNAL_CALL_SECRET`。恢复时必须提供**与备份时相同**的 KEK，否则：

- `t_channel.upstream_key_enc` 的 `v1:` 信封无法解密，网关调上游全部失败；
- `t_request_body` 的正文无法回看，日志详情页正文区块报错。

因此 KEK 必须单独托管（密钥管理系统或离线保管），不能只存在于被备份的那台机器上。

## 4 8 条对账断言的验证方法

实现位置：`internal/recon/daily.go`（`Daily.Checks()` 返回 R-01..R-08，`Daily.Run()` 执行并写 `t_reconcile_diff`）；违例检测用例在 `internal/recon/assert_test.go`；差异处置见 `docs/ops-runbook.md` §3；业务口径表见 `docs/settlement.md` §2。

| 码 | 实现函数 | 判定条件（表 / 条件） | 差异等级 |
| :--- | :--- | :--- | :--- |
| `R01_BALANCE_MISMATCH` | `checkR01` | `t_credit_account.balance_micro <> SUM(t_ledger_entry.amount_total_micro)`，按 `user_id` | CRITICAL |
| `R02_TWO_ACCOUNTS` | `checkR02` | `balance_micro <> balance_recharge_micro + balance_bonus_micro` | CRITICAL |
| `R03_LEDGER_CONTINUITY` | `checkR03` | 按 `user_id`、`id` 升序，后一条 `balance_before_micro` ≠ 前一条 `balance_after_micro` | CRITICAL |
| `R04_BATCH_REMAINING` | `checkR04` | `t_credit_batch.remaining_micro <> face_value_micro - consumed_micro` | HIGH |
| `R05_DAILY_QUOTA` | `checkR05` | `t_user_daily_quota.used_total_micro` 与当日 `SUM(t_usage_record.consumed_micro)` 偏差 > 100 微积分（> 10,000 为 CRITICAL） | HIGH / CRITICAL |
| `R06_HANGING_PREHOLD` | `checkR06` | `t_prehold_hold` 中 `state='HELD'` 且过期超过 1 小时的行数 | HIGH |
| `R07_IDEM_EXPIRED` | `checkR07` | `t_idempotency_record` 中 `expires_at < UTC_TIMESTAMP(3)` 的行数超过 100,000 | MEDIUM |
| `R08_COST_MARGIN_MISMATCH` | `checkR08` | `t_metric_model_daily` 当日 `cost_configured=1` 且 `gross_profit_micro <> consumed_micro - cost_micro` | HIGH |

另外 `Daily.Run` 会执行 `runExternal`（上游账单比对，差异类型见 `internal/recon/recon.go` 的 `DiffType` 常量），它是 §2 第 6 条「上游账单差」的实现，**不在 R-01..R-08 八个码里**，报告里要单独列。

### 4.1 在恢复库上执行的正确方式

| 方式 | 命令 | 能证明什么 | 不能证明什么 |
| :--- | :--- | :--- | :--- |
| 代码自检 | `go test ./internal/recon/ -run 'TestR0[1-8]'` | 8 个检查函数能识别违例、差异落库正确 | **不验证恢复库**：`internal/recon/testmain_test.go:28` 把 schema 硬编码为 `yunshu_recon_test`，`TEST_DB_DSN` 只影响主机与账号，不能指向 `yunshu_drill` |
| 恢复库实跑 | `bash scripts/recondrill.sh --db yunshu_drill -day 2026-10-29`：`recon.Daily.Checks()` 逐码计数 + `recon.Daily.Run()` 跑完整日对账（含 `runExternal`）并写 `t_reconcile_diff` | 恢复数据本身在 R-01..R-08 与外部账单比对下的差异数 | 不能证明 `docs/settlement.md` §2 的 8 条都通过（两套口径不对应，见 §4.2）；空表上的 0 差异不构成覆盖 |

`cmd/recondrill`（`ENV=local` 门禁，拒绝 `*_test` 与 `yunshu`，后者需 `--allow-production`）不修改测试 schema，也不搬运 fixture：`internal/recon/testmain_test.go` 的测试会插 1,000 条用量行并跑迁移，指向演练库会毁掉「恢复结果」这一前提。`scripts/recondrill.sh` 负责读 `deploy/.env.stack`、交叉编译 linux 二进制，并在 `yunshu-stack_backend` 网络的一次性容器里执行（compose 的 MySQL 不发布宿主端口；宿主 `13306` 是另一台测试库 MySQL）。2026-10-29 的演练结果见 `docs/tasks/delivery/dr-drill-2026-10-29.md`。

### 4.2 口径差异（段 2 举证前必须注意）

`docs/settlement.md` §2 的 8 条业务表述与 `daily.go` 的 R-01..R-08 **不是一一对应**：

| §2 断言 | R-01..R-08 中的对应 |
| :--- | :--- |
| 2 流水和 == 余额 | R-01 |
| 7 日额度快照一致 | R-05 |
| 1 金额守恒 | 仅由 R-03 的逐笔连续性间接覆盖，没有按日窗口的 `期初 + 流水 = 期末` 检查 |
| 3 批次余额和 == 账户余额 | **无独立检查**（R-02 查两个分账户之和，R-04 查单批次剩余） |
| 4 无孤儿流水 | **无独立检查**（R-01 从账户侧 LEFT JOIN，无账户的流水行看不到） |
| 5 无未结算成功请求 | **无独立检查**（R-06 查的是过期预扣） |
| 6 上游账单差 | `runExternal`，不在 R-01..R-08 内 |
| 8 配置版本可回溯 | **无独立检查** |

演练报告必须逐条注明「已由哪个码覆盖 / 未覆盖」，不能把 R-01..R-08 的 0 差异直接写成「§2 全部 8 条通过」。该差异需在段 2 前确认以哪套口径举证（或补检查）。

## 5 演练清单

段 2 按此表逐项执行并在报告中贴命令输出。

- [ ] `bash scripts/backup.sh` 退出 0，`backups/` 出现 `.sql.gz` + `.manifest`
- [ ] manifest 的 `schema_version=0026`，`sha256` 与 `sha256sum <dump>` 一致
- [ ] 连续执行两次备份均成功（不同时间戳，互不覆盖）
- [ ] `bash scripts/backup.sh --keep 1` 只留最新一份，旧 dump 与 manifest 一并删除
- [ ] `bash scripts/restore.sh <dump> --db yunshu_drill` 退出 0，6 张关键表全 `OK`
- [ ] 第二次恢复到 `yunshu_drill` 仍退出 0（幂等）
- [ ] 源库与 `yunshu_drill` 的 6 张关键表行数逐表相等
- [ ] `bash scripts/restore.sh <dump> --db yunshu`（无 `--yes`）退出 1 且打印拒绝原因
- [ ] `bash scripts/restore.sh <dump> --db yunshu_test` 退出 1（测试库不可恢复）
- [ ] 篡改过的 dump 被 sha256 校验拦下（退出 1，未碰目标库）
- [ ] 缺 manifest 的 dump 被拒绝
- [ ] 在 `yunshu_drill` 上跑 R-01..R-08，差异数 0；`runExternal` 结果单独记录
- [ ] 备份、恢复全流程日志里搜不到口令明文（`MYSQL_PWD` 只走环境变量）
- [ ] `bash scripts/check-eol.sh` 退出 0
- [ ] 产出 `docs/tasks/delivery/dr-drill-2026-10-29.md`，含命令、耗时、行数与差分数

## 6 已知限制

| # | 限制 | 影响 | 缓解 / 后续 |
| :--- | :--- | :--- | :--- |
| 1 | 单机 MySQL，无主从、无副本 | 实例或容器卷损坏时业务中断直到恢复完成 | 需引入副本与故障切换；不在 T36 范围 |
| 2 | 备份与库在同一台宿主（`backups/`） | 宿主磁盘损坏时库与备份一起丢 | 必须增加异地/对象存储副本（OI-09 未关闭） |
| 3 | 无静态加密 | 拿到宿主文件即可读全部用户行、密钥哈希、渠道密文 | 权限 0600/0700；上对象存储时必须 SSE-KMS |
| 4 | 逻辑备份，不是物理备份 | 稳态 745 GB 时 `mysqldump` 恢复 6–10 小时，破坏 RTO ≤ 1 小时 | 换 XtraBackup 8.0（TDS §8.1.3） |
| 5 | 无 binlog 归档 | RPO 等于备份间隔（每日 1 次时最长 24 小时） | 增加每分钟归档 + 失败告警（TDS §8.1.2） |
| 6 | 无自动调度与失败告警 | 备份可能静默停摆 | 由 cron / 任务计划程序按非 0 退出码告警 |
| 7 | manifest 行数在导出前采集 | 备份后源库有写入时，恢复行数会多于基线（显示 `NEW`） | 演练选低峰/停写窗口；以 `LOST` 作为失败判据 |
| 8 | 恢复到已存在的库不会删除库内多余表 | 旧 Schema 遗留表会留在目标库 | 演练库可先手工 `DROP DATABASE` 再恢复；真实恢复前确认目标库就是待覆盖库 |
| 9 | 恢复不自动跑迁移 | 目标库版本低于 dump 时脚本报错退出 | 先回滚代码或手工迁移，再由人决定顺序 |
| 10 | 不覆盖 Redis | 预扣、限流、快照与库可能短暂不一致 | 见 §3.2 缓存表；`controld` 重启重发快照 |
| 11 | 备份不含 KEK / 环境变量 | 换机恢复后渠道密钥与请求正文不可解密 | KEK 单独托管；恢复前确认 `.env.stack` 取值 |
| 12 | `yunshu_log` 当前 0 表 | 日志库 dump 只有 471 B 头信息 | 日志表建立后自动随备份；恢复脚本按单库执行 |
| 13 | 覆盖 `yunshu` 要求人工 `--yes`，脚本不检测服务是否已停 | 服务未停时恢复可能被并发写入污染 | 恢复流程第 1 步必须先停 gatewayd/controld/jobsd |

## 7 相关文件

| 文件 | 作用 |
| :--- | :--- |
| `scripts/backup.sh` | 全量备份、manifest、保留策略 |
| `scripts/restore.sh` | sha256 校验、目标库安全门、幂等导入、行数比对 |
| `deploy/.env.stack` | `MYSQL_ROOT_PASSWORD` 来源（不入库） |
| `deploy/docker-compose.stack.yml` | 容器 `yunshu-stack-mysql` 与 `mysql_data` 卷定义 |
| `cmd/recondrill/main.go` | 恢复库上的 R-01..R-08 + `runExternal` 入口（只写 `t_reconcile_diff` / `t_notification`） |
| `scripts/recondrill.sh` | 在栈网络容器内执行 `cmd/recondrill`，口令经 0600 env-file 注入 |
| `internal/recon/daily.go` | R-01..R-08 与 `runExternal` 实现 |
| `internal/recon/assert_test.go` | 8 条断言的违例检测用例 |
| `docs/settlement.md` §2 | 8 条断言业务口径 |
| `docs/ops-runbook.md` §3 | 差异处置与禁止操作 |
| `docs/tasks/delivery/open-issues.md` OI-09 | RPO/RTO 与演练未关闭项 |
| `docs/tasks/T36-备份恢复与DR演练.md` | 本任务卡 |
