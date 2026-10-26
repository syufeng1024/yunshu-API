-- 0012 内容安全命中记录表（TDS §8.4.4）。
-- 只存命中片段（varchar(255)），禁止 TEXT 存原始消息全文：
-- 存全文等于把用户未开启完整日志的 prompt 又落一份库，且片段可能含 PII。
-- 申诉与注销联动的字段一并保留；本表不加 deleted_at，注销靠字段清空而非软删。

CREATE TABLE t_content_policy_hit (
  id              bigint unsigned NOT NULL AUTO_INCREMENT,
  request_id      char(36)        NOT NULL COMMENT '与 X-Request-Id 一致；工单与申诉的关联键',
  user_id         bigint unsigned NOT NULL,
  api_key_id      bigint unsigned NOT NULL,
  model_name_snap varchar(128)    NOT NULL COMMENT '命中的请求模型（快照，模型改名后仍可追溯）',
  rule_id         varchar(64)     NOT NULL COMMENT '命中的规则标识（如 kw_terror_v3 / len_anomaly）',
  rule_version    varchar(32)     NOT NULL DEFAULT '' COMMENT '规则库版本；规则迭代后能还原当时按什么判的',
  category        varchar(32)     NOT NULL COMMENT 'VIOLENCE / SEXUAL / POLITICAL / ILLEGAL / PII / OTHER',
  severity        varchar(8)      NOT NULL DEFAULT 'BLOCK' COMMENT 'BLOCK / WARN（V1.0 只有 BLOCK 会拒绝）',
  matched_excerpt varchar(255)    NOT NULL DEFAULT '' COMMENT '命中片段（已脱敏，≤255 字符）；禁止存原始消息全文',
  snippet_sha256  char(64)        DEFAULT NULL COMMENT '命中片段的 SHA-256；申诉比对时不必解密',
  snippet_offset  int unsigned    NOT NULL DEFAULT 0 COMMENT '在原始 messages 中的字符偏移，用于精确回溯',
  client_ip       varbinary(16)   DEFAULT NULL,
  action          varchar(16)     NOT NULL DEFAULT 'BLOCK' COMMENT 'BLOCK / APPEAL_PENDING / APPEAL_UPHELD / APPEAL_OVERTURNED',
  appeal_at       datetime(3)     DEFAULT NULL,
  appeal_note     varchar(500)    DEFAULT NULL,
  reviewer_id     bigint unsigned DEFAULT NULL COMMENT '申诉复核人（运营），与 action 联动',
  reviewed_at     datetime(3)     DEFAULT NULL,
  created_at      datetime(3)     NOT NULL COMMENT '保留期基准：180 天（F-1）；注销时清空片段与 IP',
  PRIMARY KEY (id),
  UNIQUE KEY uk_cph_request (request_id),
  KEY idx_cph_user_created (user_id, created_at) COMMENT '账号级违规次数统计与申诉列表',
  KEY idx_cph_created (created_at) COMMENT '保留期清理（与 t_usage_record 同口径）',
  KEY idx_cph_action (action, created_at) COMMENT '申诉待办队列'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
