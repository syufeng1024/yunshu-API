-- 0009 t_plan 种子：体验版 / 标准版 / 专业版 / 企业版 / 默认档。
-- 数值唯一来源为 PRD §3.2.1：price_fen = 售价（元）× 100；
-- grant_*_micro = 积分 × 10000（MICRO_PER_CREDIT）；daily_quota_micro = 账户日额度（积分）× 10000。
-- FREE_DEFAULT 的账户日额度按 PRD §3.2.1 为「无上限」，存 NULL；
-- 其余四档存「积分 × 10000」，0 的语义是「继承上一档」。
--
-- 0007 / 0008 已预留给 t_model.enabled → listing_state 的 Expand-Contract：
-- 0007 加列回填、0008 删旧列并换索引，V1.0 不执行。加载器按版本号排序、
-- 不要求连续，因此这里可以跳号；任何有结构依赖的迁移都不许跳号。

INSERT INTO t_plan (
  code, name,
  price_fen, grant_recharge_micro, grant_bonus_micro, bonus_valid_days,
  rpm, tpm, concurrency, daily_quota_micro,
  model_scope_mode, invoice_supported, sla_text,
  permanent, stackable, sort_order, enabled,
  created_at, updated_at
) VALUES
  ('TRIAL', '体验版',
   0, 0, 70000000, 90,
   60, 126000, 22, 10000000,
   'ALL', 0, '',
   0, 0, 0, 1,
   NOW(3), NOW(3)),
  ('STANDARD', '标准版',
   9900, 99000000, 20000000, 90,
   300, 630000, 110, 50000000,
   'ALL', 0, '',
   1, 0, 1, 1,
   NOW(3), NOW(3)),
  ('PRO', '专业版',
   29900, 299000000, 90000000, 90,
   1000, 2100000, 367, 200000000,
   'ALL', 0, '',
   1, 0, 2, 1,
   NOW(3), NOW(3)),
  ('ENTERPRISE', '企业版',
   99900, 999000000, 400000000, 90,
   3000, 6300000, 1100, 1000000000,
   'ALL', 0, '',
   1, 0, 3, 1,
   NOW(3), NOW(3)),
  ('FREE_DEFAULT', '默认档',
   0, 0, 0, 90,
   60, 60000, 5, NULL,
   'ALL', 0, '',
   1, 0, 4, 1,
   NOW(3), NOW(3));
