-- ============================================================================
--  云枢 API 中转站 · 演示数据播种 04：上游渠道 + 路由 + 模型→渠道关联
-- ============================================================================
--  调用方：scripts/seed-demo.sh（用 stdin 管道送进 mysql 客户端）
--  前置变量（由脚本 SET 后传入）：
--    @proto  t_protocol 中 code='OPENAI_COMPAT' 的行 id（脚本先查，缺失即退出）
--    @now    UTC_TIMESTAMP(3)，整批数据共用一个时刻
--
--  为什么单独一步：迁移 0019 只在 ENV=local 执行，而全栈 compose 固定
--  ENV=prod，所以 t_channel / t_route 是 0 行。没有渠道时 P12 直接
--  no_available_upstream，调试台与 /v1 全部失败——与密钥是否加密无关。
--
--  upstream_key_enc：本文件只写空值占位，真正的密文由脚本随后调用
--  cmd/keytool seed-channel 写入（v1: 信封 + mask）。绝不在这里写明文。
--
--  幂等：渠道以 uk_channel_name 为准 upsert；路由以
--  uk_route_model_channel_proto_upstream 为准 upsert；不删除任何行。
--  重跑只会把 base_url / 权重 / enabled 重置回脚本值，不会清掉已加密的密钥。
--
--  region 必须是枚举大写 DOMESTIC：P12（internal/gw/p12_router.go）对非
--  DOMESTIC 的路由与渠道做硬排除，写成 cn 会得到 no_available_upstream。
-- ============================================================================

SET NAMES utf8mb4 COLLATE utf8mb4_0900_as_cs;
SET @now := UTC_TIMESTAMP(3);

-- ---------------------------------------------------------------------------
-- 1. 渠道（一个境内 OpenAI 兼容渠道；base_url 指向不可达域名，用于验证
--    错误能走到 P15 的上游失败，而不是停在选路阶段）
-- ---------------------------------------------------------------------------
INSERT INTO t_channel (
  name, protocol_id, region, base_url, upstream_key_enc, upstream_key_mask,
  group_scope_mode, weight, priority,
  timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms,
  retry_max, stream_supported, stream_usage_mode,
  enabled, created_by, created_at, updated_at
)
SELECT
  'demo-domestic-channel', p.id, 'DOMESTIC', 'https://local.invalid/v1',
  '', 'sk-****pending',
  'ALL', 100, 100,
  3000, 30000, 120000,
  2, 1, 'UNKNOWN',
  1, 0, @now, @now
FROM t_protocol p
WHERE p.code = 'OPENAI_COMPAT'
ON DUPLICATE KEY UPDATE
  protocol_id            = VALUES(protocol_id),
  region                 = VALUES(region),
  base_url               = VALUES(base_url),
  group_scope_mode       = VALUES(group_scope_mode),
  weight                 = VALUES(weight),
  priority               = VALUES(priority),
  timeout_connect_ms     = VALUES(timeout_connect_ms),
  timeout_first_byte_ms  = VALUES(timeout_first_byte_ms),
  timeout_total_ms       = VALUES(timeout_total_ms),
  retry_max              = VALUES(retry_max),
  stream_supported       = VALUES(stream_supported),
  stream_usage_mode      = VALUES(stream_usage_mode),
  enabled                = VALUES(enabled),
  deleted_at             = NULL,
  updated_at             = VALUES(updated_at);

SET @cid := (SELECT id FROM t_channel WHERE name = 'demo-domestic-channel' LIMIT 1);

-- ---------------------------------------------------------------------------
-- 2. 模型→渠道关联（运营端的「主渠道」列；路由事实来源仍是 t_route）
-- ---------------------------------------------------------------------------
UPDATE t_model
   SET primary_channel_id = @cid,
       updated_at         = @now
 WHERE model_name IN ('qwen-max', 'deepseek-chat', 'glm-4-plus', 'qwen-plus')
   AND deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- 3. 路由：4 个演示模型各 1 条，指向同一渠道
--    upstream_model_name 在 V1.0 用与模型名相同的值（真实上游名由运营维护）。
-- ---------------------------------------------------------------------------
INSERT INTO t_route (
  model_id, channel_id, protocol_id, upstream_model_name, region,
  weight, priority, stream_supported, max_tokens_ceiling, enabled,
  created_at, updated_at
)
SELECT
  m.id, @cid, p.id, m.model_name, 'DOMESTIC',
  100, 100, 1, 0, 1,
  @now, @now
FROM t_model m
JOIN t_protocol p ON p.code = 'OPENAI_COMPAT'
WHERE m.model_name IN ('qwen-max', 'deepseek-chat', 'glm-4-plus', 'qwen-plus')
  AND m.deleted_at IS NULL
ON DUPLICATE KEY UPDATE
  region         = VALUES(region),
  weight         = VALUES(weight),
  priority       = VALUES(priority),
  stream_supported = VALUES(stream_supported),
  max_tokens_ceiling = VALUES(max_tokens_ceiling),
  enabled        = VALUES(enabled),
  updated_at     = VALUES(updated_at);
