-- 0023 放宽 t_admin_audit_log.result：varchar(16) → varchar(24)，修复 E-28。
--
-- 列注释的枚举含 REJECTED_BY_GUARD（17 字符），列宽却只有 16。MySQL 8.0 默认
-- STRICT_TRANS_TABLES 下写入直接报 Error 1406 Data too long；internal/control/auth/login.go
-- 的 6 处 auditLogin 都传这个值，而 auditLogin 只 s.warn 不回泡，因此匿名、密码错、
-- 账号锁定这类被拒登录长期没有审计记录——爆破留痕静默丢失。
--
-- 放宽列宽而不是改枚举名：审计取值随后续规则增长，24 字符留出余量。
-- 16 → 24 仍在一字节长度前缀内，字符集、排序规则与默认值均不变；不回填存量行。
-- 0003 已应用且 checksum 已锁，这里只 ALTER，不改 0003。

ALTER TABLE t_admin_audit_log
  MODIFY COLUMN result varchar(24) NOT NULL DEFAULT 'OK'
    COMMENT 'OK / REJECTED_BY_GUARD / REJECTED / …；≤24 字符';
