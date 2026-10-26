-- 0018 配置键 / 价格分组 / 协议 / 限流五档种子。
-- 数值唯一来源：TDS §3.7 键表（45 个唯一键；正文「34 个键」是过期计数，见 errata E-07）、
-- PRD §3.2.1、PRD §3.5。
--
-- 先补一次 ALTER：T02 建表时 t_rate_limit_policy.daily_quota_micro 沿用了 TDS 的
-- NOT NULL DEFAULT 0，而 PRD §3.2.1 默认档是「无上限」。本卡把「无上限」统一编码为
-- NULL，必须先把列改为可空；0 在日额度语义下等于「一分钱都不能用」。
--
-- 配置键作用域固定 GLOBAL / scope_id=0。quota.overdraft_warn_days 不属五个命名空间，
-- cfg_namespace 写 UNASSIGNED（TDS §3.7 2026-10-15 裁决）。
-- idem.* 与 site.recommend_limit 按 errata E-08 归 FEATURE：幂等窗口与推荐位条数都是
-- 功能开关，不决定扣钱、路由或上游超时，不新增第六个命名空间。
-- 发票与 SLA 口径按 D-11：付费三档支持发票并承诺 7×24 工单受理；体验版与默认档为空。

ALTER TABLE t_rate_limit_policy
  MODIFY COLUMN daily_quota_micro bigint unsigned DEFAULT NULL
    COMMENT '账户日额度，微积分；NULL = 无上限（FREE_DEFAULT），0 = 无日额度（scope_type=KEY 恒 0）';

INSERT INTO t_config_item (
  cfg_namespace, cfg_key, scope, scope_id, cfg_value, version, enabled, updated_by, created_at, updated_at
) VALUES
  -- PRICING：billing.*（10）
  ('PRICING',   'billing.deduct_policy',            'GLOBAL', 0, '"fifo_expiry"',                                    1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'billing.refund_policy',            'GLOBAL', 0, '"by_order"',                                       1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'billing.overdraft_limit_default',  'GLOBAL', 0, '50',                                               1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'billing.estimate_language_coef',   'GLOBAL', 0, '{"zh":1.0,"en":1.0,"code":1.0,"mixed":1.0}',     1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'billing.prehold_ratio',            'GLOBAL', 0, '1.2',                                              1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'billing.prehold_max_ratio',        'GLOBAL', 0, '0.5',                                              1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'billing.settle_deadline_ms',       'GLOBAL', 0, '{"non_stream_ms":5000,"stream_ms":10000}',        1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'billing.bonus_expire_days',        'GLOBAL', 0, '90',                                               1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'billing.long_ctx_prehold_guard_bp','GLOBAL', 0, '9000',                                             1, 1, NULL, NOW(3), NOW(3)),
  -- 作用于 token 预估环节的 +10% 安全余量（万分比 11000），不是金额整体 ×1.1。
  ('PRICING',   'billing.prestimate_safety_bp',     'GLOBAL', 0, '11000',                                            1, 1, NULL, NOW(3), NOW(3)),
  -- PRICING：pricing.*（9）
  ('PRICING',   'pricing.cost_source',              'GLOBAL', 0, '"manual"',                                         1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'pricing.tier_chain',               'GLOBAL', 0, '"model>channel"',                                  1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'pricing.agent_markup',             'GLOBAL', 0, '"off"',                                            1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'pricing.cache_billing_enabled',    'GLOBAL', 0, 'false',                                            1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'pricing.min_margin_bp',            'GLOBAL', 0, '2500',                                             1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'pricing.plan_min_margin_bp',       'GLOBAL', 0, '1000',                                             1, 1, NULL, NOW(3), NOW(3)),
  -- V1 税率占位，恒 1.00，不参与现有计价链路；接入税制时由此键取值。
  ('PRICING',   'pricing.tax_rate_multiplier',      'GLOBAL', 0, '1.00',                                             1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'pricing.max_bonus_ratio_bp',       'GLOBAL', 0, '3000',                                             1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'pricing.group_switch_mode',        'GLOBAL', 0, '"ops_only"',                                       1, 1, NULL, NOW(3), NOW(3)),
  -- PRICING：plan.*（2）
  ('PRICING',   'plan.default_tier',                'GLOBAL', 0, '"free"',                                           1, 1, NULL, NOW(3), NOW(3)),
  ('PRICING',   'plan.trial_days',                  'GLOBAL', 0, '7',                                                1, 1, NULL, NOW(3), NOW(3)),
  -- RATELIMIT：quota.*（6；overdraft_warn_days 不归属）
  ('RATELIMIT', 'quota.daily_meter',                'GLOBAL', 0, '"prehold"',                                        1, 1, NULL, NOW(3), NOW(3)),
  ('RATELIMIT', 'quota.key_daily_mode',             'GLOBAL', 0, '"none"',                                           1, 1, NULL, NOW(3), NOW(3)),
  ('RATELIMIT', 'quota.reset_scope',                'GLOBAL', 0, '"account"',                                        1, 1, NULL, NOW(3), NOW(3)),
  ('RATELIMIT', 'quota.reset_tz',                   'GLOBAL', 0, '"Asia/Shanghai"',                                  1, 1, NULL, NOW(3), NOW(3)),
  ('RATELIMIT', 'quota.rate_limit_order',           'GLOBAL', 0, '"concurrency>rpm>tpm>daily"',                     1, 1, NULL, NOW(3), NOW(3)),
  -- FREE_DEFAULT 账户活跃 Key 上限（PRD F6-7），由密钥创建校验消费。
  ('RATELIMIT', 'quota.free_max_active_keys',       'GLOBAL', 0, '3',                                               1, 1, NULL, NOW(3), NOW(3)),
  -- FEATURE：log.*（4）
  ('FEATURE',   'log.body_mode',                    'GLOBAL', 0, '"meta_only"',                                      1, 1, NULL, NOW(3), NOW(3)),
  ('FEATURE',   'log.volume_basis',                 'GLOBAL', 0, '"meta"',                                           1, 1, NULL, NOW(3), NOW(3)),
  ('FEATURE',   'log.retention_days',               'GLOBAL', 0, '180',                                              1, 1, NULL, NOW(3), NOW(3)),
  ('FEATURE',   'log.same_instance',                'GLOBAL', 0, 'true',                                             1, 1, NULL, NOW(3), NOW(3)),
  -- FEATURE：idem.*（3）——幂等去重窗口与租约参数，按 E-08 归功能开关。
  ('FEATURE',   'idem.duplicate_window_seconds',    'GLOBAL', 0, '10',                                               1, 1, NULL, NOW(3), NOW(3)),
  ('FEATURE',   'idem.lease_ttl_seconds',           'GLOBAL', 0, '15',                                               1, 1, NULL, NOW(3), NOW(3)),
  ('FEATURE',   'idem.lease_renew_seconds',         'GLOBAL', 0, '5',                                                1, 1, NULL, NOW(3), NOW(3)),
  -- FEATURE：site.*（1）——官网推荐模型区块展示条数上限（PRD F22-6）。
  ('FEATURE',   'site.recommend_limit',             'GLOBAL', 0, '8',                                                1, 1, NULL, NOW(3), NOW(3)),
  -- UPSTREAM：gw.*（9）
  ('UPSTREAM',  'gw.max_inflight',                  'GLOBAL', 0, '8000',                                             1, 1, NULL, NOW(3), NOW(3)),
  ('UPSTREAM',  'gw.drain_deadline_ms',             'GLOBAL', 0, '120000',                                           1, 1, NULL, NOW(3), NOW(3)),
  ('UPSTREAM',  'gw.upstream_timeout_ms',           'GLOBAL', 0, '50000',                                            1, 1, NULL, NOW(3), NOW(3)),
  -- 流式首字节单次尝试超时按 TA-1.2 / errata E-01 取 30000，90s 是已作废的总预算旧值。
  ('UPSTREAM',  'gw.stream_first_byte_ms',          'GLOBAL', 0, '30000',                                            1, 1, NULL, NOW(3), NOW(3)),
  ('UPSTREAM',  'gw.stream_total_ms',               'GLOBAL', 0, '600000',                                           1, 1, NULL, NOW(3), NOW(3)),
  ('UPSTREAM',  'gw.settle_reserve_ms',             'GLOBAL', 0, '15000',                                            1, 1, NULL, NOW(3), NOW(3)),
  -- 双值键：TDS 默认值「65000（非流式）/ 45000（流式）」，单值会丢掉流式口径，
  -- 按 docs/config-keys.md 约定存 JSON 对象；消费方按请求是否流式取对应字段，
  -- 禁止把对象当整数读取。
  ('UPSTREAM',  'gw.retry_threshold_ms',            'GLOBAL', 0, '{"unary":65000,"stream":45000}',                  1, 1, NULL, NOW(3), NOW(3)),
  ('UPSTREAM',  'gw.client_timeout_budget_ms',      'GLOBAL', 0, '120000',                                           1, 1, NULL, NOW(3), NOW(3)),
  ('UPSTREAM',  'gw.retry_max',                     'GLOBAL', 0, '1',                                                1, 1, NULL, NOW(3), NOW(3)),
  -- UNASSIGNED：资金侧策略，写入应返回 config_value_invalid，直到资金/限额策略统一梳理
  ('UNASSIGNED','quota.overdraft_warn_days',        'GLOBAL', 0, '7',                                                1, 1, NULL, NOW(3), NOW(3))
ON DUPLICATE KEY UPDATE
  cfg_value = VALUES(cfg_value),
  enabled   = VALUES(enabled),
  updated_at = VALUES(updated_at);

INSERT INTO t_price_group (
  code, name, ratio_bp, model_scope_mode, channel_scope_mode, self_service,
  min_margin_bp, remark, enabled, created_by, created_at, updated_at
) VALUES
  ('standard', '标准分组', 10000, 'ALL', 'ALL', 0, NULL,
   '标准倍率 1.0（10000 万分比），全部分组可见', 1, 0, NOW(3), NOW(3)),
  ('contract', '合约分组', 8500, 'ALL', 'ALL', 0, NULL,
   '合约倍率 0.85（8500 万分比），须运营审批后开通；照常参与毛利地板校验', 1, 0, NOW(3), NOW(3)),
  ('internal', '内部测试分组', 0, 'ALL', 'ALL', 0, NULL,
   '不计费：内部账号按 0 倍率结算，不产生收入、不参与毛利地板校验；不是免费打折，仅限 is_ops 账号', 1, 0, NOW(3), NOW(3))
ON DUPLICATE KEY UPDATE
  name = VALUES(name),
  ratio_bp = VALUES(ratio_bp),
  remark = VALUES(remark),
  updated_at = VALUES(updated_at);

INSERT INTO t_protocol (
  code, name, direction, adapter_version, stream_usage_mode, body_parser, enabled, created_at, updated_at
) VALUES
  ('BAILIAN',          '百炼',            'BOTH', 'v1', 'UNKNOWN', 'IR_JSON', 1, NOW(3), NOW(3)),
  ('OPENAI_COMPAT',    'OpenAI 兼容',     'BOTH', 'v1', 'UNKNOWN', 'IR_JSON', 1, NOW(3), NOW(3)),
  ('ANTHROPIC_COMPAT', 'Anthropic 兼容',  'BOTH', 'v1', 'UNKNOWN', 'IR_JSON', 1, NOW(3), NOW(3))
ON DUPLICATE KEY UPDATE
  name = VALUES(name),
  enabled = VALUES(enabled),
  updated_at = VALUES(updated_at);

-- 五档限流模板：scope_type='USER' + scope_id=0 是哨兵，不对应任何真实用户；
-- 用 effective_from 的固定毫秒差满足 (scope_type, scope_id, effective_from) 唯一键。
-- source 写档位标识便于运营对照；运行时为用户生成的真实策略行仍用 PLAN / TRIAL / FREE_DEFAULT。
-- 额度单位：微积分（1 积分 = 10000 微积分）；FREE_DEFAULT 日额度无上限 → NULL。
INSERT INTO t_rate_limit_policy (
  scope_type, scope_id, source, rpm, tpm, concurrency_quota, daily_quota_micro,
  effective_from, effective_to, fallback_policy_id, created_at, updated_at
) VALUES
  ('USER', 0, 'TRIAL',        60,   126000,  22,   10000000,   '1970-01-01 00:00:00.000', NULL, NULL, NOW(3), NOW(3)),
  ('USER', 0, 'STANDARD',     300,  630000,  110,  50000000,   '1970-01-01 00:00:01.000', NULL, NULL, NOW(3), NOW(3)),
  ('USER', 0, 'PRO',          1000, 2100000, 367,  200000000,  '1970-01-01 00:00:02.000', NULL, NULL, NOW(3), NOW(3)),
  ('USER', 0, 'ENTERPRISE',   3000, 6300000, 1100, 1000000000, '1970-01-01 00:00:03.000', NULL, NULL, NOW(3), NOW(3)),
  ('USER', 0, 'FREE_DEFAULT', 60,   60000,   5,    NULL,       '1970-01-01 00:00:04.000', NULL, NULL, NOW(3), NOW(3))
ON DUPLICATE KEY UPDATE
  rpm = VALUES(rpm),
  tpm = VALUES(tpm),
  concurrency_quota = VALUES(concurrency_quota),
  daily_quota_micro = VALUES(daily_quota_micro),
  updated_at = VALUES(updated_at);

-- D-11 发票口径：付费三档支持发票并写 SLA 文案；体验版 / 默认档置 0 与空串。
UPDATE t_plan
SET invoice_supported = CASE
      WHEN code IN ('STANDARD', 'PRO', 'ENTERPRISE') THEN 1 ELSE 0 END,
    sla_text = CASE
      WHEN code IN ('STANDARD', 'PRO', 'ENTERPRISE')
        THEN '7×24 工单受理，不承诺可用性百分比' ELSE '' END,
    updated_at = NOW(3)
WHERE code IN ('TRIAL', 'STANDARD', 'PRO', 'ENTERPRISE', 'FREE_DEFAULT');
