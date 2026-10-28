-- 0025 余额调整单与公告-模型关联（T24 段 2）。
--
-- t_balance_adjust 是余额调整的业务单据：t_review_task 只存待办，金额与方向、
-- 理由落在本表。金额恒为正，方向由 direction 表达，避免「负金额 + 方向列」
-- 产生两种互相矛盾的真相。
--
-- pending_user_id 是生成列：state='PENDING' 时等于 user_id，其余状态为 NULL。
-- 唯一索引 uk_ba_user_pending 依赖 MySQL「唯一索引允许多个 NULL」的语义，
-- 保证同一用户同时最多一张待审单；提交路径不需要「先查后插」，也就没有
-- 两个并发提交都查到 0 行的竞态窗口。
--
-- t_announcement_model 保存公告与模型的关联。0003 的 t_announcement 没有模型列，
-- 读接口按 announcement_id IN (...) 一次聚合，不做 N+1。

CREATE TABLE t_balance_adjust (
  id              bigint unsigned NOT NULL AUTO_INCREMENT,
  user_id         bigint unsigned NOT NULL,
  direction       varchar(8)      NOT NULL COMMENT 'INCREASE / DECREASE',
  amount_micro    bigint          NOT NULL COMMENT '恒 > 0；方向由 direction 表达',
  reason          varchar(255)    NOT NULL,
  state           varchar(16)     NOT NULL DEFAULT 'PENDING' COMMENT 'PENDING / APPROVED / REJECTED',
  applicant_id    bigint unsigned NOT NULL,
  decided_by      bigint unsigned DEFAULT NULL,
  decided_at      datetime(3)     DEFAULT NULL,
  created_at      datetime(3)     NOT NULL,
  updated_at      datetime(3)     NOT NULL,
  pending_user_id bigint unsigned GENERATED ALWAYS AS (IF(state = 'PENDING', user_id, NULL)) STORED COMMENT '待审唯一槽位：非 PENDING 时为 NULL',
  PRIMARY KEY (id),
  UNIQUE KEY uk_ba_user_pending (pending_user_id),
  KEY idx_ba_user_created (user_id, created_at),
  KEY idx_ba_state_created (state, created_at),
  CONSTRAINT chk_ba_amount CHECK (amount_micro > 0),
  CONSTRAINT chk_ba_direction CHECK (direction IN ('INCREASE', 'DECREASE'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_announcement_model (
  announcement_id bigint unsigned NOT NULL,
  model_id        bigint unsigned NOT NULL,
  PRIMARY KEY (announcement_id, model_id),
  KEY idx_am_model (model_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
