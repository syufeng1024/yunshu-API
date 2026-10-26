-- 0014 欠费登记（PRD §3.1 / TDS §1.5）。
-- 单请求实际扣减上限 = 可用余额 + 透支上限；超出部分不入余额，落本表并冻结账号。
-- 金额一律微积分整数；due/repaid 的对外字段名按 T03 卡为 amount_micro / settled_micro。
-- 账实闭合：t_ledger_entry 只记真实扣减，本表只记缺口，二者相加 = 真实成本。

CREATE TABLE t_arrears (
  id                    bigint unsigned NOT NULL AUTO_INCREMENT,
  user_id               bigint unsigned NOT NULL,
  request_id            char(36)        NOT NULL COMMENT '触发欠费的请求',
  arrears_no            char(26)        NOT NULL COMMENT 'ULID，对外可引用',
  amount_micro          bigint          NOT NULL COMMENT '欠费额（正数，微积分）',
  settled_micro         bigint          NOT NULL DEFAULT 0 COMMENT '已抵扣额（微积分），不得超过 amount_micro',
  state                 varchar(16)     NOT NULL DEFAULT 'OPEN' COMMENT 'OPEN / PARTIAL / REPAID / WRITTEN_OFF',
  overdraft_limit_micro bigint          NOT NULL COMMENT '发生时的透支上限快照',
  freeze_flag           tinyint(1)      NOT NULL DEFAULT 1 COMMENT '是否触发 FROZEN_OVERDRAFT（V1.0 恒 1）',
  created_at            datetime(3)     NOT NULL,
  updated_at            datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_arr_request (request_id),
  UNIQUE KEY uk_arr_no (arrears_no),
  KEY idx_arr_user_state (user_id, state) COMMENT '充值后按用户抵扣未结清欠费'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
