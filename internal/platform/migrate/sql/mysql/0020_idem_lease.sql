-- 0020 幂等租约 Expand：给 t_idempotency_record 补 fence_token 与 lease_expires_at。
--
-- 纯 Expand，只加不删：0010 / 0013 之后的读路径不读这两列，旧代码在新 Schema 下
-- 仍按 (scope, api_key_id, idem_key) + body_hash 工作，因此新旧版本可同时在线。
-- fence_token 是租约持有者令牌，完成 / 失败写入时按 CAS 校验，防止租约被抢占后
-- 旧请求把结果写回；空串只可能来自 0020 之前写入的存量行，读取侧按「无租约」处理。
-- lease_expires_at 为 NULL 表示没有活跃租约（DONE / FAILED 或租约已被清理）。
-- 抢租约依赖 idx_idem_lease 定位过期行，避免全表扫描。

ALTER TABLE t_idempotency_record
  ADD COLUMN fence_token char(36) NOT NULL DEFAULT ''
    COMMENT '租约令牌（UUIDv7 文本）；每次抢到租约重新生成，写入时按 CAS 校验；空串 = 0020 之前写入的存量行' AFTER settled_micro,
  ADD COLUMN lease_expires_at datetime(3) DEFAULT NULL
    COMMENT '租约到期时刻；NULL = 无活跃租约。抢租约条件：lease_expires_at IS NULL OR lease_expires_at < NOW(3)' AFTER fence_token,
  ADD KEY idx_idem_lease (lease_expires_at)
    COMMENT '过期租约扫描：UPDATE ... WHERE lease_expires_at < NOW(3)';
