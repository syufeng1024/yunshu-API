-- 0015 工单 / 工单回复 / 审核任务（PRD §5.9 / §6.6，TDS §1.9）。
-- 状态机只有三态：OPEN → RESPONDED → CLOSED，CLOSED 后用户补充回复重开为 OPEN。
-- 明确不设 AUTO_CLOSED：SLA 倒计时只做展示与告警，超时自动关闭会把「平台没回」记成「已了结」。
-- first_response_due_at 是 SLA 停表基准（created_at + 4h）；重开时由业务层重算。

CREATE TABLE t_ticket (
  id                    bigint unsigned NOT NULL AUTO_INCREMENT,
  ticket_no             char(20)        NOT NULL COMMENT 'TK + 时间序，全局唯一',
  user_id               bigint unsigned NOT NULL,
  ticket_type           varchar(16)     NOT NULL COMMENT 'BILLING / TECH / CONTENT_APPEAL / OTHER',
  title                 varchar(120)    NOT NULL,
  description           text            NOT NULL COMMENT '纯文本，V1.0 不支持富文本与附件；上限 5000 字',
  request_id            char(36)        DEFAULT NULL COMMENT '可选关联；详情页一跳直达日志详情',
  policy_hit_id         bigint unsigned DEFAULT NULL COMMENT '内容申诉默认带出的命中规则 ID',
  state                 varchar(16)     NOT NULL DEFAULT 'OPEN' COMMENT 'OPEN / RESPONDED / CLOSED；不设 AUTO_CLOSED',
  first_response_due_at datetime(3)     NOT NULL COMMENT '首次响应截止（created_at + 4h；重开时重算）',
  first_responded_at    datetime(3)     DEFAULT NULL COMMENT '首次响应时点，SLA 停表',
  last_replied_at       datetime(3)     DEFAULT NULL,
  reopen_count          int unsigned    NOT NULL DEFAULT 0,
  created_at            datetime(3)     NOT NULL,
  updated_at            datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_tk_no (ticket_no),
  KEY idx_tk_user_state (user_id, state),
  KEY idx_tk_state_created (state, created_at) COMMENT '运营侧 SLA 超时排序'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_ticket_reply (
  id          bigint unsigned NOT NULL AUTO_INCREMENT,
  ticket_id   bigint unsigned NOT NULL,
  author_type varchar(8)      NOT NULL COMMENT 'USER / OPS',
  author_id   bigint unsigned NOT NULL,
  content     text            NOT NULL,
  is_internal tinyint(1)      NOT NULL DEFAULT 0 COMMENT '内部备注，用户不可见',
  created_at  datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  KEY idx_tr_ticket (ticket_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_review_task (
  id            bigint unsigned NOT NULL AUTO_INCREMENT,
  biz_type      varchar(24)     NOT NULL COMMENT 'QUOTA_APPLY / INVOICE_APPLY / REFUND_APPLY / REDEEM_BATCH / BALANCE_ADJUST',
  biz_id        bigint unsigned NOT NULL COMMENT '指向各业务表主键或调额审计 ID',
  applicant_id  bigint unsigned NOT NULL,
  state         varchar(16)     NOT NULL DEFAULT 'PENDING' COMMENT 'PENDING / APPROVED / REJECTED',
  assignee_id   bigint unsigned DEFAULT NULL COMMENT '转交只记人、不改状态',
  decision_note varchar(255)    DEFAULT NULL,
  decided_at    datetime(3)     DEFAULT NULL,
  created_at    datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_rt_biz (biz_type, biz_id),
  KEY idx_rt_state_created (state, created_at) COMMENT '审核中心 PENDING 队列按提交时间排序'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs COMMENT='审核中心统一队列；不替代各业务表，只做待办聚合与流转记录';
