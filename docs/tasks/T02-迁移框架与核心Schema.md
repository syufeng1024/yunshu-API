# T02 · 迁移框架与核心 Schema（0001–0006、0009）

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T02 |
| 阶段 | 1 · 数据层 |
| 优先级 | P0 |
| 依赖 | T01 |
| 可并行 | 与 T17 并行 |
| 交付根 | `internal/platform/migrate/`、`internal/platform/migrate/sql/mysql/` |
| 状态 | ⬜ 未开始 |

## 1. 目标

交付**可审计、可回滚、防重号**的迁移引擎，以及 54 张表中的 **47 张核心表**（0001–0006），外加 `t_plan` 种子数据（0009）。本卡完成后，"库能建起来、编号不会撞、回滚有脚本、checksum 有守卫"，后续所有卡只做增量。

> **勘误（2026-10-26 交付总监，验收时修正）**：本节与 §7 A2 原写「46 张」，系我按 `8+11+26+2` 心算时漏掉 `t_config_publish`（TDS §1.8 有定义、§1.2 分层清单未收录）。按 §4 逐表清单实际为 **47 张**；54 = 47（本卡）+ 7（T03 的 0012/0014/0015/0016/0017）。实现方发现差异后**未删表凑数**、原样交付并报回，处置正确。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| TDS §0 | 1067–1110 | 字符集 `utf8mb4_0900_as_cs`；`datetime(3)` UTC；金额 `bigint _micro`；单价 `int unsigned _micro_per_k`；`http_status smallint unsigned`；索引命名 `uk_/idx_/fk_`；软删哨兵改写 |
| TDS §1.2 | 1179–1192 | 54 张表分层清单（**本卡的范围判据**） |
| TDS §1.3 | 1195–1424 | `t_user`、`t_api_key` 等建表级定义 |
| TDS §1.4 | 1424–2023 | 配置驱动层（`t_channel` / `t_model` / `t_route` / `t_price_rule` / `t_config_*`） |
| TDS §1.5 | 2023–2419 | 资金账本（`t_ledger_entry` 的 append-only、三个唯一键、CAS 余额） |
| TDS §1.6 | 2419–2610 | 计量与幂等（`t_usage_record` / `t_upstream_attempt` / `t_idempotency_record`） |
| TDS §1.7 | 2610–2698 | 指标聚合 4 表 |
| TDS §1.8 | 2698–2884 | 运营、风控、通知 6 表 |
| TDS §8.6 | 4887–4942 | **迁移编号裁定表（唯一权威）**、checksum 机制、跳号边界 |
| PRD §3.2.1 | 281–326 | 套餐档位数值（0009 种子来源） |

## 3. 前置校验

- T01 验收通过（`make up` 后 MySQL 8.0 可达）。
- 目录 `internal/platform/migrate/sql/mysql/` 为空或不存在（全新建）。

## 4. 迁移编号裁定（本项目已定，**不得自行改动**）

| 编号 | 内容 | 涉及表 | 本卡 |
| :--- | :--- | :--- | :--- |
| `0001` | 账号与凭据 | `t_user`、`t_verify_record`、`t_device_fingerprint`、`t_verification_code`、`t_api_key`、`t_api_key_model`、`t_api_key_ip_rule`、`t_api_key_audit` | ✅ |
| `0002` | 配置驱动层（**不含** scope 列、不含价格分组） | `t_protocol`、`t_channel`、`t_model`、`t_model_alias`、`t_model_capability`、`t_route`、`t_price_rule`、`t_plan`、`t_config_item`、`t_config_runtime`、`t_config_publish` | ✅ |
| `0003` | 资金 / 计量 / 限流 / 指标 / 运营 | `t_credit_account`、`t_credit_batch`、`t_ledger_entry`、`t_prehold_hold`、`t_settle_task`、`t_recharge_order`、`t_refund_order`、`t_redeem_batch`、`t_redeem_code`、`t_invoice_apply`、`t_quota_apply`、`t_usage_record`、`t_upstream_attempt`、`t_idempotency_record`、`t_rate_limit_policy`、`t_user_daily_quota`、`t_metric_minute`、`t_metric_daily`、`t_metric_model_daily`、`t_metric_channel_health`、`t_admin_audit_log`、`t_risk_rule`、`t_risk_hit_log`、`t_announcement`、`t_announcement_read`、`t_notification` | ✅ |
| `0004` | `t_config_item` 增 `scope` / `scope_id` 两列 + 索引（Expand 第一步） | `t_config_item` | ✅ |
| `0005` | `t_config_item` 换唯一键（旧 `uk` → `uk_cfg_ns_key_scope`） | `t_config_item` | ✅ |
| `0006` | 价格分组 | `t_price_group`、`t_price_group_scope` + `t_api_key.group_code` | ✅ |
| `0007` | **预留，永久不创建**（`t_model.enabled→listing_state` Expand） | — | ❌ 不建 |
| `0008` | **预留，永久不创建**（同 Contract 步） | — | ❌ 不建 |
| `0009` | `t_plan` 种子（四档 + 默认档） | `t_plan` | ✅ |
| `0010`–`0017` | 见 T03 | — | ❌ T03 |

> `0007`/`0008` 不创建的理由（D-01）：全新建库的 `t_model` 直接以 `listing_state` 建表，无需 Expand-Contract；但两个号已被 TDS §8.6 写死为「预留未排期」，**不得挪作他用**。引擎必须容忍跳号。

## 5. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `internal/platform/migrate/migrate.go` | 加载器 + 执行器：按版本号排序、**不要求连续**；先算 `sha256(文件全文)` 再比对 `schema_migrations.checksum`，不符即报错退出（**不得静默重放**）；重复版本号在启动时报错，错误文案含占用者文件名 |
| 2 | `.../migrate.go` 内 `schema_migrations` | 列：`version char(4) PK`、`name varchar(128)`、`checksum char(64)`、`applied_at datetime(3)`、`execution_ms int`、`success tinyint(1)` |
| 3 | `.../load.go` | 从 `embed.FS` 读 `sql/mysql/*.sql`，配对 `*.down.sql`；缺 down 文件即报错 |
| 4 | `.../migrate_test.go` | 6 条断言：① 版本号唯一 ② 版本号连续跳号只在 `0007`/`0008` 处出现 ③ 每个 up 有 down ④ checksum 变更检测生效 ⑤ 重复版本号报错 ⑥ 迁移条数 == 文件数（`!= N` 断言，随新增同步） |
| 5 | `.../guard_test.go` | 静态守卫：`sql/mysql/` 下文件名必须匹配 `^\d{4}_[a-z0-9_]+\.sql$`；不得出现 `0007_` / `0008_` 前缀 |
| 6 | `sql/mysql/0001_account_credential.sql` + `.down.sql` | 8 张账号与凭据表 |
| 7 | `sql/mysql/0002_config_driver.sql` + `.down.sql` | 11 张配置表，`t_model` 直接含 `listing_state enum('LISTED','CATALOG_ONLY','DELISTED')`、`region enum('DOMESTIC','OVERSEAS')`、`published_at datetime(3)`。**不含 `recommend_weight`**（属 T03 的 `0017`） |
| 8 | `sql/mysql/0003_fund_usage_ops.sql` + `.down.sql` | 26 张资金/计量/限流/指标/运营表 |
| 9 | `sql/mysql/0004_config_scope_expand.sql` + `.down.sql` | `ALTER TABLE t_config_item ADD COLUMN scope varchar(16) NOT NULL DEFAULT 'GLOBAL', ADD COLUMN scope_id bigint unsigned NOT NULL DEFAULT 0` |
| 10 | `sql/mysql/0005_config_scope_uk.sql` + `.down.sql` | 唯一键替换为 `uk_cfg_ns_key_scope (cfg_namespace, cfg_key, scope, scope_id)` |
| 11 | `sql/mysql/0006_price_group.sql` + `.down.sql` | `t_price_group`（`ratio_bp int unsigned`）、`t_price_group_scope`、`ALTER TABLE t_api_key ADD group_code varchar(32) NOT NULL DEFAULT 'standard'` |
| 12 | `sql/mysql/0009_seed_plan.sql` + `.down.sql` | 5 行 `t_plan` 种子（TRIAL/STANDARD/PRO/ENTERPRISE/FREE_DEFAULT） |
| 13 | `cmd/migrate/main.go` 或 `make migrate` | 命令入口，支持 `up` / `down N` / `status` |
| 14 | `docs/schema.md` | 54 表总表：表名 → 迁移编号 → 分层 → 关键唯一键（用于后续卡索引，**不复制 DDL**） |

## 6. 硬约束（违反即驳回）

1. **字符集/排序规则**：全库 `utf8mb4` / `utf8mb4_0900_as_cs`。模型名、别名、Key 名**大小写敏感**（`qwen-Max ≠ qwen-max`）。
2. **时间列**：一律 `datetime(3)` 且存 UTC；**禁止 `timestamp`**（`grep -c "timestamp" sql/mysql/*.sql` 必须为 0）。
3. **金额列**：一律 `bigint` 且列名后缀 `_micro`；**禁止 `decimal` / `float` / `double` 承载金额**。例外：`t_metric_*` 的比率列 `success_rate decimal(7,4)`。
4. **单价列**：`int unsigned` 且后缀 `_micro_per_k`。
5. **`http_status`**：`smallint unsigned`。
6. **`t_ledger_entry` 三个唯一键齐备**：`uk_le_idem`、`uk_le_entry_no`、`PRIMARY KEY(id)`。**均不得包含 `created_at`**（一旦含分区列，唯一性保护即失效）。
7. **账本 append-only 由表权限与触发器双保险**：对应用账号不授 `UPDATE` / `DELETE`；DDL 中不得出现 `ON UPDATE CURRENT_TIMESTAMP`。
8. **软删哨兵**：参与唯一索引的列（`email` / `phone` / `key_hash` / `model_name`）删除时改写为 `#del#{id}#{unix_ms}`，不用 `NULL` 置空。
9. **唯一键 / 索引命名**：`uk_<表短名>_<列>` / `idx_<表短名>_<列>` / `fk_<表短名>_<父表短名>`。
10. **不建外键约束**（`FOREIGN KEY` 一律不写）：命名规则只为可读性；一致性由应用层事务保证，避免迁移期锁表与级联删除风险。
11. **每个 up 必须有 down**；`0009` 的 down 为 `DELETE FROM t_plan WHERE code IN (...)`。
12. **迁移文件一旦提交不得改注释**（checksum 覆盖全文，改一个字即 checksum 失败）。
13. **不得创建 `0007` / `0008`**。
14. **DDL 来源可追溯**：TDS §1.x 已给出字段的，字段名/类型/默认值/COMMENT **逐字对齐**；TDS 未给出完整 DDL 的表，字段必须来自该表在 TDS 其它章节的引用，并在回执中列「表名 → 字段 → TDS 行号」。
15. **`t_plan` 种子数值与 PRD §3.2.1 完全一致**：TRIAL 0/0/7000、STANDARD 99/9900/2000、PRO 299/29900/9000、ENTERPRISE 999/99900/40000、FREE_DEFAULT（RPM 60 / TPM 60000 / 并发 5 / 日额度 NULL）。**TPM = RPM × 2100**（默认档不适用此比例）。

## 7. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `make migrate` 全绿，`schema_migrations` 有 7 行（0001–0006、0009） | 复跑 + SQL |
| A2 | 业务表 **47** 张（`information_schema` 计数含 `schema_migrations` 共 48） | SQL |
| A3 | `SHOW VARIABLES LIKE 'collation_database'` == `utf8mb4_0900_as_cs`；`SHOW TABLE STATUS` 中 46 张表 collation 全为 `utf8mb4_0900_as_cs` | SQL |
| A4 | `grep -c "timestamp" sql/mysql/*.sql` == 0 | grep |
| A5 | `grep -nE "decimal|float|double" sql/mysql/*.sql` 仅命中 `success_rate` 一处 | grep |
| A6 | `grep -n "FOREIGN KEY" sql/mysql/*.sql` 为空 | grep |
| A7 | `ls sql/mysql/ | grep -E "^000[78]_"` 为空 | shell |
| A8 | `down` 全部回滚到空库后再 `up` 成功（幂等） | 复跑 |
| A9 | 改一个迁移文件的注释后重启迁移器 → **checksum 报错**并退出码非 0 | 手工验证 |
| A10 | 临时复制一个迁移并改成已有版本号 → **重复版本号报错**，文案含两文件名 | 手工验证 |
| A11 | `go test ./internal/platform/migrate/...` 6+ 条断言全绿 | 复跑 |
| A12 | `t_plan` 5 行数值与 PRD §3.2.1 逐列比对一致 | SQL 抽样 |
| A13 | `t_ledger_entry` 三唯一键存在，且均不含 `created_at` | `SHOW INDEX` |
| A14 | `docs/schema.md` 覆盖 54 表且标出哪些属 0010–0017（尚未创建） | 人工 |

## 8. 自检命令

```bash
go test ./internal/platform/migrate/... -v
make migrate && docker exec -i $(docker compose -f deploy/docker-compose.yml ps -q mysql) \
  mysql -uroot -proot yunshu -N -e "SELECT version,name FROM schema_migrations ORDER BY version"
docker exec -i $(docker compose -f deploy/docker-compose.yml ps -q mysql) \
  mysql -uroot -proot -N -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='yunshu'"
grep -c "timestamp" internal/platform/migrate/sql/mysql/*.sql
grep -rn "FOREIGN KEY"          internal/platform/migrate/sql/mysql/ || echo OK
ls internal/platform/migrate/sql/mysql/ | grep -E "^000[78]_" || echo "OK: 0007/0008 未占用"
```

> **环境注记（2026-10-26 交付总监补充）**：本机无 GNU `make`，且宿主 3306/6379 被占用。等价执行方式：
> `scripts/dev.sh up`（自动套用端口覆盖文件）→ `scripts/dev.sh migrate up` → 容器名直查 `docker exec yunshu-mysql ...`。
> 覆盖后宿主端口为 `13306`/`16379`，容器内仍是 3306/6379，不影响任何断言。**不得**改用本机 MySQL 8.4。

## 9. 禁止事项

- 不得为"图省事"跳过 down 脚本。
- 不得把 `0007`/`0008` 用于幂等或任何其它表（幂等是 T03 的 `0010`）。
- 不得把外键、触发器用于业务一致性（唯一例外：T22 可加账本防改触发器，本卡不加）。
- 不得在迁移里写业务数据（种子仅限 `0009` 的 `t_plan`，其余种子属 T03）。
- 不得引入 ORM 自动迁移（gorm AutoMigrate 等）——只允许显式 SQL。

## 10. 完成回执（已归档 · 2026-10-26）

> 归档说明：同 T01，当时回执未落入卡内，2026-10-26 统一补档。

| 项 | 内容 |
| :--- | :--- |
| 卡号 | T02 |
| 完成状态 | 完成（14 项交付物落地） |
| 验收结论 | ⚠️ 有条件通过（3 项遗留，均非阻塞） |
| 复核人 | 交付总监 |

**迁移清单**：`0001`（8 表）、`0002`（11 表）、`0003`（26 表）、`0004`/`0005`（`t_config_item` 作用域两步）、`0006`（2 表 + `t_api_key.group_code`）、`0009`（`t_plan` 5 行种子）。`0007`/`0008` **未创建**（永久预留）。

**自检结果**

```
schema_migrations            7 行，全部 APPLIED / CHECKSUM MATCH
业务表                        47（information_schema 48，含 schema_migrations）
collation 例外               0（全 utf8mb4_0900_as_cs）
timestamp 字样               0 命中
FOREIGN KEY                  0 命中
decimal                      仅 t_metric_daily.success_rate
t_ledger_entry 唯一键         PRIMARY(id) / uk_le_idem / uk_le_entry_no，均不含 created_at
up/down 对称                 8/8、11/11、26/26、2/2
引擎测试                     14 条用例全绿
checksum 篡改                报错退出码 1
重复版本号                   报错文案含两个文件名
```

**偏离与遗留**：① 表数 **47 而非卡面 46**——卡内逐表清单 `8+11+26+2=47`，是卡片 §1 的算术错误，实现方未删表凑数；② `t_plan.daily_quota_micro` 改可空（E-04）；③ `t_user.cancel_apply_at` 替代 TDS 的中文列名（E-03）；④ 账本账号 `UPDATE`/`DELETE` 权限留待 T22。
