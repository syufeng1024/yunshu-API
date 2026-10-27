-- ============================================================================
--  云枢 API 中转站 · 演示数据播种 01：平台内容（模型主数据 + 价目 + 能力矩阵）
-- ============================================================================
--  调用方：scripts/seed-demo.sh（用 stdin 管道送进 mysql 客户端）
--  前置变量（由脚本 SET 后传入）：
--    @uid    演示账号 t_user.id
--    @proto  t_protocol 中 code='OPENAI_COMPAT' 的行 id（脚本先查，缺失即退出）
--    @now    UTC_TIMESTAMP(3)，整批数据共用一个时刻
--
--  幂等策略：
--    * t_model 以 uk_model_name 为业务键 upsert —— 行 id 跨次运行保持稳定，
--      t_usage_record.model_id / t_price_rule.scope_id 因此不会漂移。
--    * t_price_rule 没有业务唯一键，改为「删 + 插」，删除范围严格限定在
--      本脚本拥有的 4 个模型 id 之下（scope_level='MODEL' AND scope_id IN (...)
--      AND created_by=0）。这 4 行模型本身就是本脚本建的，价格归播种脚本所有：
--      重跑会把价格重置回脚本值。运营若要在演示库里长期改价，请改本文件。
--    * t_model_capability 以 uk_mc_model_cap (model_id, cap_code) upsert。
--
--  依赖语法：INSERT ... VALUES ... AS new ON DUPLICATE KEY UPDATE（MySQL 8.0.19+），
--  与 deploy/docker-compose.stack.yml 里的 mysql:8.0 镜像一致。
-- ============================================================================

SET NAMES utf8mb4 COLLATE utf8mb4_0900_as_cs;
SET @now := UTC_TIMESTAMP(3);

-- ---------------------------------------------------------------------------
-- 1. 模型主数据：4 个已上架文本模型
--    recommend_weight 刻意取 100/80/60/40 四个不同值，用于验证
--    /api/public/models 与 /api/console/models 的「权重大者优先」排序。
--    capability_mask 位掩码：bit0 STREAM / bit1 TOOL_CALL / bit2 VISION / bit3 JSON_MODE
--    （15 = 流式 + 工具 + 视觉 + JSON；11 = 不含视觉）
-- ---------------------------------------------------------------------------
INSERT INTO t_model (
  model_name, display_name, region, model_type, protocol_id,
  context_length, capability_mask, listing_state, recommend_weight,
  published_at, created_at, updated_at, deleted_at
) VALUES
  ('qwen-max',      '通义千问 Max',    'DOMESTIC', 'TEXT', @proto,  32768, 15, 'LISTED', 100, @now - INTERVAL 30 DAY, @now - INTERVAL 30 DAY, @now, NULL),
  ('deepseek-chat', 'DeepSeek Chat',   'DOMESTIC', 'TEXT', @proto,  65536, 11, 'LISTED',  80, @now - INTERVAL 45 DAY, @now - INTERVAL 45 DAY, @now, NULL),
  ('glm-4-plus',    '智谱 GLM-4-Plus', 'DOMESTIC', 'TEXT', @proto, 131072, 11, 'LISTED',  60, @now - INTERVAL 20 DAY, @now - INTERVAL 20 DAY, @now, NULL),
  ('qwen-plus',     '通义千问 Plus',   'DOMESTIC', 'TEXT', @proto, 131072, 15, 'LISTED',  40, @now - INTERVAL 60 DAY, @now - INTERVAL 60 DAY, @now, NULL)
AS new
ON DUPLICATE KEY UPDATE
  display_name     = new.display_name,
  region           = new.region,
  model_type       = new.model_type,
  protocol_id      = new.protocol_id,
  context_length   = new.context_length,
  capability_mask  = new.capability_mask,
  listing_state    = new.listing_state,
  delisted_at      = NULL,
  recommend_weight = new.recommend_weight,
  deleted_at       = NULL,
  updated_at       = new.updated_at;

-- 4 个模型的 id（uk_model_name 保证唯一）
SET @m1 := (SELECT id FROM t_model WHERE model_name = 'qwen-max'      LIMIT 1);
SET @m2 := (SELECT id FROM t_model WHERE model_name = 'deepseek-chat' LIMIT 1);
SET @m3 := (SELECT id FROM t_model WHERE model_name = 'glm-4-plus'    LIMIT 1);
SET @m4 := (SELECT id FROM t_model WHERE model_name = 'qwen-plus'     LIMIT 1);

-- ---------------------------------------------------------------------------
-- 2. 当前生效的价目规则
--    单价单位：微积分 / 千 token。1 积分 = 10000 微积分，1 元 = 100 积分。
--    1500 微/千 token = 0.15 积分/千 token = 1.5 元/百万 token。
--    解析条件是「enabled=1 + effective_from <= now + effective_to IS NULL」，
--    与 internal/billing.PriceRule.ActiveAt 和 /api/public/models 的子查询一致。
-- ---------------------------------------------------------------------------
DELETE FROM t_price_rule
 WHERE scope_level = 'MODEL'
   AND scope_id IN (@m1, @m2, @m3, @m4);

INSERT INTO t_price_rule (
  scope_level, scope_id, model_type,
  in_price_micro_per_k, out_price_micro_per_k, per_call_price_micro,
  cache_hit_ratio_bp, cache_write_ratio_bp,
  long_ctx_threshold_tokens, long_ctx_in_price_micro_per_k, long_ctx_out_price_micro_per_k,
  cost_in_micro_per_k, cost_out_micro_per_k, cost_per_call_micro,
  markup_factor_bp, version, effective_from, effective_to, enabled, created_by, created_at
) VALUES
  ('MODEL', @m1, 'TEXT', 1500, 6000, 0, NULL, NULL, NULL, NULL, NULL, 1100, 4200, NULL, 10000, 1, @now - INTERVAL 90 DAY, NULL, 1, 0, @now),
  ('MODEL', @m2, 'TEXT', 1000, 3000, 0, NULL, NULL, NULL, NULL, NULL,  700, 2100, NULL, 10000, 1, @now - INTERVAL 90 DAY, NULL, 1, 0, @now),
  ('MODEL', @m3, 'TEXT', 2500, 10000, 0, NULL, NULL, NULL, NULL, NULL, 1800, 7000, NULL, 10000, 1, @now - INTERVAL 90 DAY, NULL, 1, 0, @now),
  ('MODEL', @m4, 'TEXT',  800, 2000, 0, NULL, NULL, NULL, NULL, NULL,  560, 1400, NULL, 10000, 1, @now - INTERVAL 90 DAY, NULL, 1, 0, @now);

-- t_model.default_price_rule_id 只是快捷指针，唯一价格写源是 t_price_rule；
-- 这里回填指针，让运营端「默认价目」列不为空。
UPDATE t_model m
  JOIN t_price_rule r
    ON r.scope_level = 'MODEL' AND r.scope_id = m.id AND r.enabled = 1 AND r.effective_to IS NULL
   SET m.default_price_rule_id = r.id
 WHERE m.id IN (@m1, @m2, @m3, @m4);

-- ---------------------------------------------------------------------------
-- 3. 能力矩阵（控制台模型详情页）
--    capability_mask 与这里的 supported 必须同义：
--    qwen-max / qwen-plus 支持视觉（mask bit2 = 1），另两个不支持。
-- ---------------------------------------------------------------------------
INSERT INTO t_model_capability (model_id, cap_code, supported, note, updated_at)
SELECT t2.model_id, t2.cap_code, t2.supported, t2.note, t2.updated_at
  FROM (
        SELECT m.id AS model_id,
               c.cap_code,
               CASE WHEN c.cap_code = 'VISION' THEN m.vision ELSE 1 END AS supported,
               CASE WHEN c.cap_code = 'VISION' AND m.vision = 0
                    THEN 'V1.0 未开通视觉理解' ELSE NULL END AS note,
               @now AS updated_at
          FROM (
                SELECT @m1 AS id, 1 AS vision UNION ALL
                SELECT @m2, 0 UNION ALL
                SELECT @m3, 0 UNION ALL
                SELECT @m4, 1
               ) m
          CROSS JOIN (
                SELECT 'STREAM'    AS cap_code UNION ALL
                SELECT 'TOOL_CALL' UNION ALL
                SELECT 'VISION'    UNION ALL
                SELECT 'JSON_MODE'
               ) c
       ) AS t2
-- INSERT ... SELECT 里 JOIN 的 ON 会和 ON DUPLICATE KEY 抢解析，
-- 因此把取数包成派生表 t2，再引用 t2.* 写 upsert（MySQL 官方给的消歧写法）。
ON DUPLICATE KEY UPDATE
  supported  = t2.supported,
  note       = t2.note,
  updated_at = t2.updated_at;
