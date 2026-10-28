-- 0022 请求正文落库：新增 t_request_body 一张表。
--
-- 只存密文与 body_key_id，明文绝不落库。body_key_id 指向独立于
-- CHANNEL_KEY_KEK 的正文 KEK（LOG_BODY_KEK），两类密文使用不同 AAD，
-- 不能互换解密。
--
-- request_id 与 t_usage_record.request_id 同值。0003 把该列定义为 char(36)
-- （ULID/UUIDv7，对外 X-Request-Id），本表沿用同一宽度；收窄成 char(26)
-- 会让读取端用 36 位 request_id 查不到行。
--
-- created_at / expires_at 由写入端显式赋值：迁移文件不得出现时间戳关键字
-- （guard_test.go 的迁移关键词守卫），既有表同样不建库级默认值。
-- expires_at = created_at + log.retention_days，清理任务按 expires_at 批删。
--
-- 不建外键：一致性由应用层保证，与 0001–0025 口径一致。

CREATE TABLE t_request_body (
  id          bigint unsigned NOT NULL AUTO_INCREMENT,
  request_id  char(36)        NOT NULL COMMENT '与 t_usage_record.request_id 同值',
  user_id     bigint unsigned NOT NULL,
  ciphertext  mediumblob      NOT NULL COMMENT 'AES-256-GCM 信封密文，绝不存明文',
  body_key_id varchar(32)     NOT NULL COMMENT '正文独立密钥标识，不得为空',
  created_at  datetime(3)     NOT NULL COMMENT '写入端显式赋值',
  expires_at  datetime(3)     NOT NULL COMMENT 'created_at + log.retention_days',
  PRIMARY KEY (id),
  UNIQUE KEY uk_rb_request_user (request_id, user_id),
  KEY idx_rb_expire (expires_at),
  KEY idx_rb_user_created (user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
