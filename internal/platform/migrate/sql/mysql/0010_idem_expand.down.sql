-- 0010 回滚：新键与 body_hash 可安全撤销，因为旧键 uk_idem 仍在（0013 尚未应用）。
-- 守卫：先对 uk_idem 做一次 DROP + ADD。若 0013 已应用，uk_idem 已被删除，
-- 本语句立即失败，避免回滚出一个「没有任何唯一键」的幂等表；若存在跨 Key 撞键，
-- 重建旧键也会失败——两种失败都比静默丢约束安全。

ALTER TABLE t_idempotency_record
  DROP INDEX uk_idem,
  ADD UNIQUE KEY uk_idem (scope, idem_key);

ALTER TABLE t_idempotency_record
  DROP INDEX uk_idem_key,
  DROP COLUMN body_hash,
  MODIFY COLUMN api_key_id bigint unsigned NOT NULL
    COMMENT 'Key 软删后仍指向原行（B-39 含已删除筛选）';
