-- 生产禁止执行。本文件只用于本地联调（ENV=local）：
-- 1 条境内渠道 + 2 个文本模型（各含 1 个别名）+ 2 条路由 + 2 条模型级价目规则。
-- migrate 执行器在 ENV != local 时跳过本版本并打 WARN（不报错中断，避免生产升级失败）。
-- 渠道 Base URL 与上游 Key 均为占位值，不可用于真实调用。

INSERT INTO t_channel (
  name, protocol_id, region, base_url, upstream_key_enc, upstream_key_mask,
  group_scope_mode, weight, priority, enabled, created_by, created_at, updated_at
)
SELECT
  'local-domestic-channel', p.id, 'DOMESTIC', 'https://local.invalid/v1',
  X'6C6F63616C2D6465762D706C616365686F6C646572', 'sk-****local',
  'ALL', 100, 100, 1, 0, NOW(3), NOW(3)
FROM t_protocol p
WHERE p.code = 'OPENAI_COMPAT'
ON DUPLICATE KEY UPDATE
  protocol_id = VALUES(protocol_id),
  region = VALUES(region),
  updated_at = VALUES(updated_at);

INSERT INTO t_model (
  model_name, display_name, region, model_type, protocol_id, primary_channel_id,
  context_length, capability_mask, listing_state, published_at, created_at, updated_at
)
SELECT
  'local-qwen-plus', '本地 Qwen Plus', 'DOMESTIC', 'TEXT', p.id, c.id,
  32768, 1, 'LISTED', NOW(3), NOW(3), NOW(3)
FROM t_protocol p
JOIN t_channel c ON c.name = 'local-domestic-channel'
WHERE p.code = 'OPENAI_COMPAT'
ON DUPLICATE KEY UPDATE
  display_name = VALUES(display_name),
  primary_channel_id = VALUES(primary_channel_id),
  updated_at = VALUES(updated_at);

INSERT INTO t_model (
  model_name, display_name, region, model_type, protocol_id, primary_channel_id,
  context_length, capability_mask, listing_state, published_at, created_at, updated_at
)
SELECT
  'local-deepseek-chat', '本地 DeepSeek Chat', 'DOMESTIC', 'TEXT', p.id, c.id,
  65536, 1, 'LISTED', NOW(3), NOW(3), NOW(3)
FROM t_protocol p
JOIN t_channel c ON c.name = 'local-domestic-channel'
WHERE p.code = 'OPENAI_COMPAT'
ON DUPLICATE KEY UPDATE
  display_name = VALUES(display_name),
  primary_channel_id = VALUES(primary_channel_id),
  updated_at = VALUES(updated_at);

INSERT INTO t_model_alias (alias_name, model_id, source, enabled, created_at)
SELECT 'local-qwen', m.id, 'OPS', 1, NOW(3)
FROM t_model m WHERE m.model_name = 'local-qwen-plus'
ON DUPLICATE KEY UPDATE
  model_id = VALUES(model_id),
  enabled = VALUES(enabled);

INSERT INTO t_model_alias (alias_name, model_id, source, enabled, created_at)
SELECT 'local-deepseek', m.id, 'OPS', 1, NOW(3)
FROM t_model m WHERE m.model_name = 'local-deepseek-chat'
ON DUPLICATE KEY UPDATE
  model_id = VALUES(model_id),
  enabled = VALUES(enabled);

INSERT INTO t_route (
  model_id, channel_id, protocol_id, upstream_model_name, region,
  weight, priority, stream_supported, max_tokens_ceiling, enabled, created_at, updated_at
)
SELECT
  m.id, c.id, p.id, 'qwen-plus', 'DOMESTIC',
  100, 100, 1, 0, 1, NOW(3), NOW(3)
FROM t_model m
JOIN t_channel c ON c.name = 'local-domestic-channel'
JOIN t_protocol p ON p.code = 'OPENAI_COMPAT'
WHERE m.model_name = 'local-qwen-plus'
ON DUPLICATE KEY UPDATE
  weight = VALUES(weight),
  enabled = VALUES(enabled),
  updated_at = VALUES(updated_at);

INSERT INTO t_route (
  model_id, channel_id, protocol_id, upstream_model_name, region,
  weight, priority, stream_supported, max_tokens_ceiling, enabled, created_at, updated_at
)
SELECT
  m.id, c.id, p.id, 'deepseek-chat', 'DOMESTIC',
  100, 100, 1, 0, 1, NOW(3), NOW(3)
FROM t_model m
JOIN t_channel c ON c.name = 'local-domestic-channel'
JOIN t_protocol p ON p.code = 'OPENAI_COMPAT'
WHERE m.model_name = 'local-deepseek-chat'
ON DUPLICATE KEY UPDATE
  weight = VALUES(weight),
  enabled = VALUES(enabled),
  updated_at = VALUES(updated_at);

-- t_price_rule 无业务唯一键，用 NOT EXISTS 保证「同一模型同一版本只写一条」。
-- 单价单位：微积分 / 千 token；成本价留 NULL（本地不参与毛利地板校验）。
INSERT INTO t_price_rule (
  scope_level, scope_id, model_type, in_price_micro_per_k, out_price_micro_per_k,
  per_call_price_micro, effective_from, effective_to, version, enabled, created_by, created_at
)
SELECT
  'MODEL', m.id, 'TEXT', 8000, 20000, 0,
  '2026-01-01 00:00:00.000', NULL, 1, 1, 0, NOW(3)
FROM t_model m
WHERE m.model_name = 'local-qwen-plus'
  AND NOT EXISTS (
    SELECT 1 FROM t_price_rule p
    WHERE p.scope_level = 'MODEL' AND p.scope_id = m.id AND p.version = 1
  );

INSERT INTO t_price_rule (
  scope_level, scope_id, model_type, in_price_micro_per_k, out_price_micro_per_k,
  per_call_price_micro, effective_from, effective_to, version, enabled, created_by, created_at
)
SELECT
  'MODEL', m.id, 'TEXT', 2000, 8000, 0,
  '2026-01-01 00:00:00.000', NULL, 1, 1, 0, NOW(3)
FROM t_model m
WHERE m.model_name = 'local-deepseek-chat'
  AND NOT EXISTS (
    SELECT 1 FROM t_price_rule p
    WHERE p.scope_level = 'MODEL' AND p.scope_id = m.id AND p.version = 1
  );

UPDATE t_model m
JOIN t_price_rule p
  ON p.scope_level = 'MODEL' AND p.scope_id = m.id AND p.version = 1 AND p.enabled = 1
SET m.default_price_rule_id = p.id
WHERE m.model_name IN ('local-qwen-plus', 'local-deepseek-chat')
  AND m.default_price_rule_id IS NULL;
