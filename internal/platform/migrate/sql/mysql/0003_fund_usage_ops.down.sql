-- 0003 回滚：按建表倒序删 26 张资金 / 计量 / 限流 / 指标 / 运营表。
-- t_ledger_entry 与两张审计表只允许 DROP，不允许先 UPDATE / DELETE。

DROP TABLE IF EXISTS t_notification;
DROP TABLE IF EXISTS t_announcement_read;
DROP TABLE IF EXISTS t_announcement;
DROP TABLE IF EXISTS t_risk_hit_log;
DROP TABLE IF EXISTS t_risk_rule;
DROP TABLE IF EXISTS t_admin_audit_log;
DROP TABLE IF EXISTS t_metric_channel_health;
DROP TABLE IF EXISTS t_metric_model_daily;
DROP TABLE IF EXISTS t_metric_daily;
DROP TABLE IF EXISTS t_metric_minute;
DROP TABLE IF EXISTS t_user_daily_quota;
DROP TABLE IF EXISTS t_rate_limit_policy;
DROP TABLE IF EXISTS t_idempotency_record;
DROP TABLE IF EXISTS t_upstream_attempt;
DROP TABLE IF EXISTS t_usage_record;
DROP TABLE IF EXISTS t_quota_apply;
DROP TABLE IF EXISTS t_invoice_apply;
DROP TABLE IF EXISTS t_refund_order;
DROP TABLE IF EXISTS t_redeem_code;
DROP TABLE IF EXISTS t_redeem_batch;
DROP TABLE IF EXISTS t_recharge_order;
DROP TABLE IF EXISTS t_settle_task;
DROP TABLE IF EXISTS t_prehold_hold;
DROP TABLE IF EXISTS t_ledger_entry;
DROP TABLE IF EXISTS t_credit_batch;
DROP TABLE IF EXISTS t_credit_account;
