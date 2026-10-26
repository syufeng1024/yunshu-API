-- 0011 t_user.phone_hmac：手机号唯一性迁到不可逆哈希列。
-- 原因（TDS §8.5 R-1）：注销时 phone 置 NULL 后无法做「同一手机号 90 天内不得再领体验版」判重；
-- 改为 phone=NULL, phone_hmac=HMAC(phone, PHONE_HMAC_KEY)，判重查 phone_hmac。
-- phone 明文列保留但去唯一键；唯一性由 uk_user_phone_hmac 承担（NULL 可重复，无手机号账号不受影响）。

ALTER TABLE t_user
  MODIFY COLUMN phone varchar(20) DEFAULT NULL
    COMMENT 'E.164 去 + 号；注销时置 NULL。唯一性在 phone_hmac 上，本列不再加唯一键',
  ADD COLUMN phone_hmac char(64) DEFAULT NULL
    COMMENT 'HMAC-SHA256(手机号, PHONE_HMAC_KEY) hex；用于唯一性与防重，不可逆' AFTER phone,
  DROP INDEX uk_user_phone,
  ADD UNIQUE KEY uk_user_phone_hmac (phone_hmac);
