# Schema 总表

库：`yunshu`（MySQL 8.0，`utf8mb4` / `utf8mb4_0900_as_cs`）。迁移文件在 `internal/platform/migrate/sql/mysql/`，只允许显式 SQL，无 ORM 自动迁移。

## 表计数口径

| 口径 | 数量 | 说明 |
| :--- | ---: | :--- |
| TDS §1.2 分层清单 | 46 | 只覆盖 §1.2 表格内列出的表，不含 `t_config_publish` |
| T02 实际建表（0001–0006） | 47 | 0002 组按任务卡另含 `t_config_publish`（TDS §1.8 `2866` 行定义） |
| T03 待建（0010–0017） | 7 | `t_content_policy_hit`、`t_arrears`、`t_ticket`、`t_ticket_reply`、`t_review_task`、`t_upstream_bill`、`t_channel_dedicated_user` |
| 设计总量 | 54 | 47 + 7 |
| T02 完成后 `information_schema.tables` | 48 | 47 张业务表 + `schema_migrations` |
| T03 完成后 `information_schema.tables` | 55 | 54 张业务表 + `schema_migrations` |

任务卡 §1 / §7 A2 写「46 张」，与卡内 §4 逐表清单（8 + 11 + 26 + 2 = 47）相差 1；差的是 `t_config_publish`。本表按逐表清单登记，未删表凑数。`information_schema` 计数含迁移记录表，因此比业务表多 1。

## 编号占用

| 编号 | 内容 | 状态 |
| :--- | :--- | :--- |
| 0001 | 账号与凭据（8 表） | 已落盘 |
| 0002 | 配置驱动层（11 表） | 已落盘 |
| 0003 | 资金 / 计量 / 限流 / 指标 / 运营（26 表） | 已落盘 |
| 0004 | `t_config_item` 加 `scope` / `scope_id`（Expand） | 已落盘 |
| 0005 | `t_config_item` 唯一键换为 `uk_cfg_ns_key_scope`（Contract） | 已落盘 |
| 0006 | 价格分组两表 + `t_api_key.group_code` | 已落盘 |
| 0007 | `t_model.enabled → listing_state` Expand | 永久预留，不创建 |
| 0008 | `t_model` Contract 删旧列 | 永久预留，不创建 |
| 0009 | `t_plan` 五档种子 | 已落盘 |
| 0010–0019 | 幂等、专属渠道、工单、种子 | T03 |

## 业务表

「迁移」列是创建该表的迁移；后被 `0004` / `0006` 改过结构的表在备注中标出。唯一键只列业务唯一键，`PRIMARY KEY(id)` 不重复登记。

| 表 | 迁移 | 分层 | 关键唯一键 |
| :--- | :--- | :--- | :--- |
| `t_user` | 0001 | 账号 | `uk_user_no`、`uk_user_email`、`uk_user_phone`、`uk_user_idno` |
| `t_verify_record` | 0001 | 账号 | — |
| `t_device_fingerprint` | 0001 | 账号 | `uk_fp_hash_user` |
| `t_verification_code` | 0001 | 账号 | — |
| `t_api_key` | 0001 | 凭据 | `uk_key_hash`；`0006` 加 `group_code` / `idx_key_group` |
| `t_api_key_model` | 0001 | 凭据 | `uk_akm_key_model` |
| `t_api_key_ip_rule` | 0001 | 凭据 | `uk_akip_key_cidr` |
| `t_api_key_audit` | 0001 | 凭据 | — |
| `t_protocol` | 0002 | 配置 | `uk_proto_code` |
| `t_channel` | 0002 | 配置 | `uk_channel_name` |
| `t_model` | 0002 | 配置 | `uk_model_name` |
| `t_model_alias` | 0002 | 配置 | `uk_alias_name` |
| `t_model_capability` | 0002 | 配置 | `uk_mc_model_cap` |
| `t_route` | 0002 | 配置 | `uk_route_model_channel_proto_upstream` |
| `t_price_rule` | 0002 | 配置 | — |
| `t_plan` | 0002 | 配置 | `uk_plan_code` |
| `t_config_item` | 0002 | 配置 | `0005` 后为 `uk_cfg_ns_key_scope` |
| `t_config_runtime` | 0002 | 配置 | `uk_cr_runtime_key` |
| `t_config_publish` | 0002 | 配置 | `uk_cp_publish_id` |
| `t_price_group` | 0006 | 配置 | `uk_pg_code` |
| `t_price_group_scope` | 0006 | 配置 | `uk_pgs_group_scope` |
| `t_credit_account` | 0003 | 资金 | `uk_ca_user` |
| `t_credit_batch` | 0003 | 资金 | `uk_cb_no` |
| `t_ledger_entry` | 0003 | 资金 | `uk_le_idem`、`uk_le_entry_no` |
| `t_prehold_hold` | 0003 | 资金 | `uk_ph_request` |
| `t_settle_task` | 0003 | 资金 | `uk_st_request_type` |
| `t_recharge_order` | 0003 | 资金 | `uk_ro_order_no`、`uk_ro_pay_txn` |
| `t_refund_order` | 0003 | 资金 | `uk_rf_no` |
| `t_redeem_batch` | 0003 | 资金 | — |
| `t_redeem_code` | 0003 | 资金 | `uk_rc_hash` |
| `t_invoice_apply` | 0003 | 资金 | `uk_ia_no` |
| `t_quota_apply` | 0003 | 资金 | `uk_qa_no` |
| `t_usage_record` | 0003 | 计量 | `uk_ur_request` |
| `t_upstream_attempt` | 0003 | 计量 | `uk_ua_request_attempt` |
| `t_idempotency_record` | 0003 | 计量 | `uk_idem`；`0010` 将追加 `uk_idem_key`，`0013` 删除 `uk_idem` |
| `t_rate_limit_policy` | 0003 | 限流 | `uk_rlp_scope_active` |
| `t_user_daily_quota` | 0003 | 限流 | `uk_udq_user_date` |
| `t_metric_minute` | 0003 | 指标 | `uk_mm_bucket_scope` |
| `t_metric_daily` | 0003 | 指标 | `uk_md_date_scope` |
| `t_metric_model_daily` | 0003 | 指标 | `uk_mmd_date_model` |
| `t_metric_channel_health` | 0003 | 指标 | `uk_mch_bucket_channel` |
| `t_admin_audit_log` | 0003 | 运营 | — |
| `t_risk_rule` | 0003 | 运营 | `uk_rr_name` |
| `t_risk_hit_log` | 0003 | 运营 | — |
| `t_announcement` | 0003 | 运营 | — |
| `t_announcement_read` | 0003 | 运营 | `uk_ar_ann_user` |
| `t_notification` | 0003 | 运营 | — |
| `t_content_policy_hit` | 0012（未创建） | 运营 | 待 T03 |
| `t_arrears` | 0014（未创建） | 资金 | 待 T03 |
| `t_ticket` | 0015（未创建） | 运营 | 待 T03 |
| `t_ticket_reply` | 0015（未创建） | 运营 | 待 T03 |
| `t_review_task` | 0015（未创建） | 运营 | 待 T03 |
| `t_upstream_bill` | 0016（未创建） | 资金 | 待 T03 |
| `t_channel_dedicated_user` | 0017（未创建） | 配置 | 待 T03 |

## 持有字段的迁移

| 迁移 | 改表 | 变更 |
| :--- | :--- | :--- |
| 0004 | `t_config_item` | 加 `scope varchar(16) NOT NULL DEFAULT 'GLOBAL'`、`scope_id bigint unsigned NOT NULL DEFAULT 0` |
| 0005 | `t_config_item` | `DROP INDEX uk_cfg_ns_key`，`ADD UNIQUE uk_cfg_ns_key_scope (cfg_namespace, cfg_key, scope, scope_id)` |
| 0006 | `t_api_key` | 加 `group_code varchar(32) NOT NULL DEFAULT 'standard'`、`idx_key_group (group_code, status)` |
| 0009 | `t_plan` | 插入 TRIAL / STANDARD / PRO / ENTERPRISE / FREE_DEFAULT 五行 |

## 全局约束

| 项 | 规则 |
| :--- | :--- |
| 字符集 | 每张表显式 `DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs` |
| 时间列 | 只用 `datetime(3)`，存 UTC；全仓 SQL 无 `timestamp` |
| 金额 | `bigint` + `_micro`；单价 `int unsigned` + `_micro_per_k`；唯一小数例外 `t_metric_daily.success_rate decimal(7,4)` |
| 人民币分 | TDS 明确定义为 `*_fen int unsigned`（`t_plan.price_fen`、`t_recharge_order.pay_amount_fen`、`t_refund_order.refund_fen` 等），按 TDS 保留 |
| 外键 | 全库不建；一致性由应用层事务保证 |
| 软删 | 参与唯一索引的 `email` / `phone` / `key_hash` / `model_name` 删除时改写为 `#del#{id}#{unix_ms}` |
| 账本 | `t_ledger_entry` 只追加；三个唯一键 `PRIMARY KEY(id)`、`uk_le_idem`、`uk_le_entry_no` 均不含 `created_at` |
