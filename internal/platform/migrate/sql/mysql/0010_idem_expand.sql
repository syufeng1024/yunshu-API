-- 0010 幂等 Expand：加 body_hash 与新唯一键 uk_idem_key。
-- 新键 (scope, api_key_id, idem_key) 严格弱于旧键 (scope, idem_key)：旧键成立则新键必成立，
-- 因此先加必然成功；旧键 uk_idem 在 0013 才删除，顺序不可颠倒。
--
-- body_hash 不回填：空串代表本列新增前的存量记录，读取侧降级即可
-- （24h 去重窗口自然淘汰，回填 1.7 亿行不划算）。
-- api_key_id 必须 NOT NULL DEFAULT 0：NULL 在唯一索引中互不冲突，会让新键失效。

ALTER TABLE t_idempotency_record
  MODIFY COLUMN api_key_id bigint unsigned NOT NULL DEFAULT 0
    COMMENT 'Key 软删后仍指向原行；0 = 本列新增前的存量或系统内部写入，不得为 NULL（NULL 会让唯一键失效）',
  ADD COLUMN body_hash char(64) NOT NULL DEFAULT ''
    COMMENT '请求体 SHA-256（hex）；空串 = 0010 之前的存量记录，读取侧降级' AFTER idem_key,
  ADD UNIQUE KEY uk_idem_key (scope, api_key_id, idem_key)
    COMMENT '跨 Key 隔离后的幂等键；0013 删除旧键 uk_idem';
