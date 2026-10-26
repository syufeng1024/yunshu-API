-- 0018 回滚：精确删除本迁移写入的种子行，再恢复 t_rate_limit_policy 的列约束。
-- 禁止 TRUNCATE；不删除运营后续新增的配置键 / 分组 / 协议 / 策略行。

DELETE FROM t_config_item
WHERE scope = 'GLOBAL' AND scope_id = 0
  AND (
    (cfg_namespace = 'PRICING'    AND cfg_key IN (
      'billing.deduct_policy', 'billing.refund_policy', 'billing.overdraft_limit_default',
      'billing.estimate_language_coef', 'billing.prehold_ratio', 'billing.prehold_max_ratio',
      'billing.settle_deadline_ms', 'billing.bonus_expire_days',
      'billing.long_ctx_prehold_guard_bp', 'billing.prestimate_safety_bp',
      'pricing.cost_source', 'pricing.tier_chain',
      'pricing.agent_markup', 'pricing.cache_billing_enabled', 'pricing.min_margin_bp',
      'pricing.plan_min_margin_bp', 'pricing.tax_rate_multiplier', 'pricing.max_bonus_ratio_bp',
      'pricing.group_switch_mode', 'plan.default_tier', 'plan.trial_days'))
 OR (cfg_namespace = 'RATELIMIT' AND cfg_key IN (
      'quota.daily_meter', 'quota.key_daily_mode', 'quota.reset_scope',
      'quota.reset_tz', 'quota.rate_limit_order', 'quota.free_max_active_keys'))
 OR (cfg_namespace = 'FEATURE'   AND cfg_key IN (
      'log.body_mode', 'log.volume_basis', 'log.retention_days', 'log.same_instance',
      'idem.duplicate_window_seconds', 'idem.lease_ttl_seconds', 'idem.lease_renew_seconds',
      'site.recommend_limit'))
 OR (cfg_namespace = 'UPSTREAM'  AND cfg_key IN (
      'gw.max_inflight', 'gw.drain_deadline_ms', 'gw.upstream_timeout_ms',
      'gw.stream_first_byte_ms', 'gw.stream_total_ms', 'gw.settle_reserve_ms',
      'gw.retry_threshold_ms', 'gw.client_timeout_budget_ms', 'gw.retry_max'))
 OR (cfg_namespace = 'UNASSIGNED' AND cfg_key = 'quota.overdraft_warn_days')
  );

DELETE FROM t_price_group
WHERE code IN ('standard', 'contract', 'internal');

DELETE FROM t_protocol
WHERE code IN ('BAILIAN', 'OPENAI_COMPAT', 'ANTHROPIC_COMPAT');

DELETE FROM t_rate_limit_policy
WHERE scope_type = 'USER' AND scope_id = 0
  AND source IN ('TRIAL', 'STANDARD', 'PRO', 'ENTERPRISE', 'FREE_DEFAULT')
  AND effective_from IN (
    '1970-01-01 00:00:00.000', '1970-01-01 00:00:01.000', '1970-01-01 00:00:02.000',
    '1970-01-01 00:00:03.000', '1970-01-01 00:00:04.000'
  );

-- 种子行删完后才能把 NULL 收回 NOT NULL；若运营另有 NULL 行，本语句会失败并保留现场。
ALTER TABLE t_rate_limit_policy
  MODIFY COLUMN daily_quota_micro bigint unsigned NOT NULL DEFAULT 0;

UPDATE t_plan
SET invoice_supported = 0, sla_text = '', updated_at = NOW(3)
WHERE code IN ('TRIAL', 'STANDARD', 'PRO', 'ENTERPRISE', 'FREE_DEFAULT');
