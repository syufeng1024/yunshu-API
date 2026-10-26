-- 0011 回滚：恢复 phone 唯一键并删除 phone_hmac。
-- 若已有多个账号的 phone 为 NULL，MySQL 唯一键允许多个 NULL，不会阻塞；
-- 若存在重复非 NULL 手机号，本语句会失败并保留现场，需人工清理后再回滚。

ALTER TABLE t_user
  DROP INDEX uk_user_phone_hmac,
  DROP COLUMN phone_hmac,
  ADD UNIQUE KEY uk_user_phone (phone);
