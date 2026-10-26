-- 0021 后台任务选主与对账差异：新增 t_job_lock、t_reconcile_diff 两张表。
--
-- errata E-15：§4.6 与 §5.4.1 定义的两张表在 0001–0020 中从未创建。本迁移只建这
-- 两张表，不改任何既有表；纯 Expand，可单独回滚。
--
-- t_job_lock 以 uk_jl_job(job_name) 保证一个任务只有一份租约。owner_id 是实例
-- 标识；lease_until 到期前由持有者每 20s 续约，续约失败或进程退出后其他实例可
-- 接管。released_at 非 NULL 表示正常让锁，下一次抢锁不再看 lease_until。
--
-- t_reconcile_diff 以 uk_rd_key(diff_key) 收敛差异：diff_key =
-- sha256(biz_date|diff_type|subject)。subject 对单笔是 request_id 或 trade_no，
-- 对汇总是固定汇总键；因此可空列不参与唯一键。
-- match_mode：DAILY / HOURLY / APPROX；status：OPEN / HANDLING / CLOSED。

CREATE TABLE t_job_lock (
  id          bigint unsigned NOT NULL AUTO_INCREMENT,
  job_name    varchar(64)     NOT NULL COMMENT '任务名；与 internal/jobs 注册表一致',
  owner_id    varchar(64)     NOT NULL COMMENT '当前持有者实例标识（hostname + pid）',
  lease_until datetime(3)     NOT NULL COMMENT '租约到期时刻；持有者每 20s 续约，lease 90s',
  released_at datetime(3)     DEFAULT NULL COMMENT '正常退出让锁时刻；NULL = 仍在持有或已过期',
  created_at  datetime(3)     NOT NULL,
  updated_at  datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_jl_job (job_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_reconcile_diff (
  id             bigint unsigned NOT NULL AUTO_INCREMENT,
  diff_key       char(64)        NOT NULL COMMENT 'sha256(biz_date|diff_type|subject)，同日同类型同主体只落一条',
  biz_date       date            NOT NULL COMMENT '对账自然日（UTC+8）',
  request_id     char(36)        DEFAULT NULL COMMENT '单笔差异的 request_id；汇总差异为 NULL',
  trade_no       varchar(64)     DEFAULT NULL COMMENT '支付渠道交易号；支付对账差异使用',
  diff_type      varchar(24)     NOT NULL COMMENT 'TOKEN_MISMATCH / AMOUNT_MISMATCH / MISSING_LOCAL / MISSING_UPSTREAM / BALANCE_DRIFT 等',
  local_value    bigint          DEFAULT NULL COMMENT '平台侧值；单位随 diff_type（token 数或微积分）',
  upstream_value bigint          DEFAULT NULL COMMENT '上游/渠道侧值；口径同 local_value',
  delta_micro    bigint          NOT NULL DEFAULT 0 COMMENT '差额（微积分）；token 差异按结算价折算后写入',
  match_mode     varchar(16)     NOT NULL DEFAULT 'DAILY' COMMENT 'DAILY / HOURLY / APPROX（无 request_id 时的近似匹配）',
  status         varchar(16)     NOT NULL DEFAULT 'OPEN' COMMENT 'OPEN / HANDLING / CLOSED',
  detected_at    datetime(3)     NOT NULL COMMENT '首次检出时刻；重跑不更新，delta 变化时由对账任务另写新行',
  created_at     datetime(3)     NOT NULL,
  updated_at     datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_rd_key (diff_key),
  KEY idx_rd_date_type (biz_date, diff_type, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
