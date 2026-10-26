-- 0020 回滚：纯 Expand 的逆操作。
--
-- 前置条件（正确性，不是性能）：代码已回滚到不读 fence_token / lease_expires_at
-- 的版本。仍在使用租约的进程回滚后会丢失 fencing，旧请求可能覆盖新结果。
-- 同一 ALTER 内先删索引再删列：idx_idem_lease 只覆盖 lease_expires_at，
-- 索引必须先消失，否则删列时 MySQL 会自动处理索引，后续 DROP INDEX 报 1091。

ALTER TABLE t_idempotency_record
  DROP INDEX idx_idem_lease,
  DROP COLUMN lease_expires_at,
  DROP COLUMN fence_token;
