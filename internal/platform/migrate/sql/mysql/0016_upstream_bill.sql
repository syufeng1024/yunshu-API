-- 0016 上游账单原始行（TDS §5.4.1，PRD §9.11）。
-- 对账任务只读拉取上游账单后写本表，适配器不回写 t_usage_record / t_ledger_entry。
-- 唯一性按「渠道 + 账期 + 账单行号」：单单按渠道+账期唯一会只剩一行汇总，无法承载原始明细。
-- bill_no 是上游账单号与 request_id 的统一落点，缺失时由适配器生成稳定行号。

CREATE TABLE t_upstream_bill (
  id                bigint unsigned NOT NULL AUTO_INCREMENT,
  channel_id        bigint unsigned NOT NULL,
  bill_adapter      varchar(24)     NOT NULL COMMENT 'BAILIAN / DEEPSEEK / MANUAL_UPLOAD',
  bill_period       varchar(16)     NOT NULL COMMENT '账期，如 2026-10-25 或 2026-10',
  bill_no           varchar(64)     NOT NULL COMMENT '账单行唯一号（上游单号 / request_id / 导入行号）',
  request_id        char(36)        DEFAULT NULL COMMENT '上游侧 request_id；缺失时对账按时间窗+模型+token 近似匹配',
  upstream_trade_no varchar(64)     DEFAULT NULL COMMENT '上游交易号',
  model_name        varchar(128)    DEFAULT NULL,
  tokens_in         int unsigned    NOT NULL DEFAULT 0,
  tokens_out        int unsigned    NOT NULL DEFAULT 0,
  amount_micro      bigint          NOT NULL DEFAULT 0 COMMENT '上游账单金额（微积分，CNY 口径）',
  currency          varchar(8)      NOT NULL DEFAULT 'CNY',
  raw_row           json            DEFAULT NULL COMMENT '上游原始行，保留全部字段供复核',
  created_at        datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_ub_channel_period_line (channel_id, bill_period, bill_no),
  KEY idx_ub_request (request_id) COMMENT '单笔对账按上游 request_id 命中',
  KEY idx_ub_period (bill_period) COMMENT '按账期汇总比对'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
