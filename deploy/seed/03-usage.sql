-- ============================================================================
--  云枢 API 中转站 · 演示数据播种 03：请求日志与用量
-- ============================================================================
--  调用方：scripts/seed-demo.sh
--  前置变量：@uid（演示账号 t_user.id）
--  前置条件：该账号至少有一个未删除的 t_api_key（脚本先通过控制台 API 建，
--            失败时回落到等价语义的 SQL 插入），因为 t_usage_record.api_key_id
--            是 NOT NULL。
--
--  8 条用量：今天 3 条（2 成功 + 1 失败）、1 天前 2 条、2 天前 2 条（含 1 失败）、
--  3 天前 1 条。失败行的 consumed_micro 恒 0（§3.1），成功行的消耗不是写死的，
--  而是按 internal/billing.CostMicro 的同一条公式现算：
--      consumed_micro = (tokens_in × p_in + tokens_out × p_out + 999) DIV 1000
--  单价取自 01-platform.sql 插入的当前生效价目，因此「token × 单价 = 积分」
--  在界面上永远自洽。6 笔成功消耗合计 435,000 微积分，与 02-credit.sql 的
--  6 条 CONSUME_SETTLE 流水逐笔对齐（由 99-summary.sql 的断言 A4 校验）。
--
--  幂等策略：先按 request_id 哨兵 'demo-seed-%' 精确删除（含上游尝试行），
--  再整批重插；request_id 也是 uk_ur_request 的唯一键，双重兜底。
-- ============================================================================

SET NAMES utf8mb4 COLLATE utf8mb4_0900_as_cs;
SET @now := UTC_TIMESTAMP(3);

SET @cst_midnight := DATE(@now + INTERVAL 8 HOUR) - INTERVAL 8 HOUR;
SET @today_span   := TIMESTAMPDIFF(SECOND, @cst_midnight, @now);
SET @t_today_a    := @cst_midnight + INTERVAL (@today_span DIV 4) SECOND;
SET @t_today_b    := @cst_midnight + INTERVAL (@today_span DIV 2) SECOND;
SET @t_today_c    := @cst_midnight + INTERVAL ((@today_span * 3) DIV 4) SECOND;

SET @t_req08 := @now - INTERVAL 3 DAY - INTERVAL 4 HOUR;
SET @t_req06 := @now - INTERVAL 2 DAY - INTERVAL 1 HOUR;
SET @t_req07 := @now - INTERVAL 2 DAY - INTERVAL 7 HOUR;
SET @t_req04 := @now - INTERVAL 1 DAY - INTERVAL 6 HOUR;
SET @t_req05 := @now - INTERVAL 1 DAY - INTERVAL 2 HOUR;

-- 模型 id（01-platform.sql 已 upsert）
SET @m1 := (SELECT id FROM t_model WHERE model_name = 'qwen-max'      LIMIT 1);
SET @m2 := (SELECT id FROM t_model WHERE model_name = 'deepseek-chat' LIMIT 1);
SET @m3 := (SELECT id FROM t_model WHERE model_name = 'glm-4-plus'    LIMIT 1);
SET @m4 := (SELECT id FROM t_model WHERE model_name = 'qwen-plus'     LIMIT 1);

-- 日志的 api_key_id 取该账号最早的一把 Key；api_key_name_snap 存快照，
-- 与 internal/control/logq 的展示口径一致（Key 删除后仍能显示历史名字）。
SET @key_id   := (SELECT id FROM t_api_key WHERE user_id = @uid AND deleted_at IS NULL ORDER BY id ASC LIMIT 1);
SET @key_name := (SELECT key_name FROM t_api_key WHERE id = @key_id);

-- 通道：演示库没有任何 t_channel 行（ENV=prod 时迁移 0019 不播种占位渠道），
-- 而 t_usage_record / t_upstream_attempt 的 channel 列都是 NOT NULL 且无外键。
-- 这里用 channel_id=1 + channel_name_snap 快照表达「演示上游」：
-- 日志页展示的是快照列，不 JOIN t_channel，所以不会出现空名字。
SET @demo_channel_id := 1;
SET @demo_channel    := 'demo-upstream-primary';
SET @demo_route_id   := 1;

-- ---------------------------------------------------------------------------
-- 1. 清掉上一轮播种的日志与上游尝试（只删演示哨兵行）
-- ---------------------------------------------------------------------------
DELETE FROM t_upstream_attempt
 WHERE user_id = @uid AND request_id LIKE 'demo-seed-%';

DELETE FROM t_usage_record
 WHERE user_id = @uid AND request_id LIKE 'demo-seed-%';

-- ---------------------------------------------------------------------------
-- 2. 用量/日志 8 条
-- ---------------------------------------------------------------------------
INSERT INTO t_usage_record (
  request_id, user_id, api_key_id, api_key_name_snap, model_id, model_name_snap, request_model_req,
  inbound_protocol, channel_id, channel_name_snap, outbound_protocol, route_id, price_rule_id,
  group_code_snap, config_version, http_status, biz_code, result, fail_stage, fail_owner,
  tokens_in, tokens_out, tokens_in_est, tokens_out_est, usage_source,
  prehold_micro, consumed_micro, consumed_recharge_micro, consumed_bonus_micro, refunded_micro,
  upstream_attempts, is_stream, total_ms, upstream_ms, client_ip, user_agent,
  client_aborted, full_log_saved, volume_basis, created_at
)
SELECT p.request_id, @uid, @key_id, @key_name, p.model_id, p.model_name, p.model_name,
       'OPENAI_COMPAT', @demo_channel_id, @demo_channel, 'OPENAI_COMPAT', @demo_route_id, p.rule_id,
       'standard', 7, p.http_status, p.biz_code, p.result, p.fail_stage, p.fail_owner,
       p.tokens_in, p.tokens_out, p.tokens_in_est, p.tokens_out_est, p.usage_source,
       p.cost_micro, p.cost_micro, 0, p.cost_micro, 0,
       p.attempts, 0, p.total_ms, p.upstream_ms,
       INET6_ATON('203.0.113.24'), 'yunshu-quickstart/1.0 (curl 8.4.0)',
       0, 1, 'meta', p.created_at
  FROM (
        SELECT v.*, m.model_name AS model_name, pr.id AS rule_id,
               IF(v.result = 'SUCCESS',
                  (v.tokens_in * pr.in_price_micro_per_k
                   + v.tokens_out * pr.out_price_micro_per_k + 999) DIV 1000,
                  0) AS cost_micro
          FROM (
                -- 今天 · 成功
                SELECT 'demo-seed-req-01' AS request_id, @m1 AS model_id, 'SUCCESS' AS result,
                       200 AS http_status, '' AS biz_code,
                       CAST(NULL AS CHAR(24)) AS fail_stage, CAST(NULL AS CHAR(16)) AS fail_owner,
                       12000 AS tokens_in, 4000 AS tokens_out,
                       12150 AS tokens_in_est, 3980 AS tokens_out_est,
                       'UPSTREAM_API' AS usage_source, 1 AS attempts,
                       4820 AS total_ms, 4600 AS upstream_ms, @t_today_a AS created_at
                UNION ALL
                -- 今天 · 成功
                SELECT 'demo-seed-req-02', @m2, 'SUCCESS', 200, '',
                       CAST(NULL AS CHAR(24)), CAST(NULL AS CHAR(16)),
                       30000, 8000, 30400, 7960,
                       'UPSTREAM_API', 1, 9100, 8850, @t_today_b
                UNION ALL
                -- 今天 · 失败（上游首字节超时 → 换路由重试 → 仍然失败）
                SELECT 'demo-seed-req-03', @m3, 'FAILED', 504, 'upstream_timeout',
                       'UPSTREAM', 'UPSTREAM',
                       0, 0, 820, 0,
                       'NONE', 2, 61350, 61350, @t_today_c
                UNION ALL
                -- 1 天前 · 成功
                SELECT 'demo-seed-req-04', @m1, 'SUCCESS', 200, '',
                       CAST(NULL AS CHAR(24)), CAST(NULL AS CHAR(16)),
                       8000, 2000, 8100, 1990,
                       'UPSTREAM_API', 1, 2210, 2050, @t_req04
                UNION ALL
                -- 1 天前 · 成功（长上下文批次任务）
                SELECT 'demo-seed-req-05', @m4, 'SUCCESS', 200, '',
                       CAST(NULL AS CHAR(24)), CAST(NULL AS CHAR(16)),
                       200000, 40000, 201500, 39900,
                       'UPSTREAM_API', 1, 45200, 44800, @t_req05
                UNION ALL
                -- 2 天前 · 成功
                SELECT 'demo-seed-req-06', @m2, 'SUCCESS', 200, '',
                       CAST(NULL AS CHAR(24)), CAST(NULL AS CHAR(16)),
                       30000, 5000, 30100, 4990,
                       'UPSTREAM_API', 1, 6400, 6100, @t_req06
                UNION ALL
                -- 2 天前 · 失败（转换阶段平台侧判定，归因 PLATFORM）
                SELECT 'demo-seed-req-07', @m1, 'FAILED', 502, 'upstream_invalid_response',
                       'CONVERT', 'PLATFORM',
                       0, 0, 3100, 0,
                       'NONE', 1, 2410, 1900, @t_req07
                UNION ALL
                -- 3 天前 · 成功
                SELECT 'demo-seed-req-08', @m3, 'SUCCESS', 200, '',
                       CAST(NULL AS CHAR(24)), CAST(NULL AS CHAR(16)),
                       6000, 1500, 6050, 1490,
                       'UPSTREAM_API', 1, 3120, 2980, @t_req08
               ) v
          JOIN t_model m ON m.id = v.model_id
          JOIN t_price_rule pr
            ON pr.scope_level = 'MODEL' AND pr.scope_id = v.model_id
           AND pr.enabled = 1 AND pr.effective_to IS NULL AND pr.effective_from <= @now
       ) p;

-- ---------------------------------------------------------------------------
-- 3. 上游尝试时间线（demo-seed-req-03 的两次重试）
--    第一次：首字节超时（可重试）→ RETRY_NEXT_ROUTE
--    第二次：换路后上游 502（不可重试）→ NO_RETRY，请求终态 FAILED
-- ---------------------------------------------------------------------------
INSERT INTO t_upstream_attempt (
  request_id, attempt_index, user_id, model_id, route_id, channel_id, channel_name_snap,
  protocol_code, upstream_model, phase, result, http_status, upstream_code, upstream_msg,
  is_retryable, retry_decision, connect_ms, first_byte_ms, total_ms,
  tokens_in, tokens_out, bytes_in, bytes_out, error_class, created_at
) VALUES
  ('demo-seed-req-03', 1, @uid, @m3, @demo_route_id, @demo_channel_id, @demo_channel,
   'OPENAI_COMPAT', 'glm-4-plus-upstream', 'FIRST_BYTE', 'FAILED', 504, 'timeout',
   'upstream first byte timeout after 60000ms', 1, 'RETRY_NEXT_ROUTE', 118, NULL, 60012,
   0, 0, 4096, 0, 'TIMEOUT', @t_today_c),
  ('demo-seed-req-03', 2, @uid, @m3, @demo_route_id, @demo_channel_id, @demo_channel,
   'OPENAI_COMPAT', 'glm-4-plus-upstream', 'CONNECT', 'FAILED', 502, 'internal_error',
   'upstream returned 502 bad gateway', 0, 'NO_RETRY', 44, NULL, 1338,
   0, 0, 512, 0, 'HTTP_5XX', @t_today_c + INTERVAL 62 SECOND);
