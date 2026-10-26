-- 0001 回滚：按建表倒序删 8 张账号与凭据表。
-- 全库不建外键，顺序只保证可读；IF EXISTS 允许在已部分回滚的库上重跑。

DROP TABLE IF EXISTS t_api_key_audit;
DROP TABLE IF EXISTS t_api_key_ip_rule;
DROP TABLE IF EXISTS t_api_key_model;
DROP TABLE IF EXISTS t_api_key;
DROP TABLE IF EXISTS t_verification_code;
DROP TABLE IF EXISTS t_device_fingerprint;
DROP TABLE IF EXISTS t_verify_record;
DROP TABLE IF EXISTS t_user;
