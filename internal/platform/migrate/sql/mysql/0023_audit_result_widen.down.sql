-- 0023 回滚：result 从 varchar(24) 收窄回 varchar(16)。
--
-- 收窄前必须先确认存量数据没有超过 16 字符的值。直接 MODIFY 在
-- STRICT_TRANS_TABLES 下会报 1406；放宽 sql_mode 则会把 REJECTED_BY_GUARD
-- 截断成无意义前缀，而审计表不接受静默改写已有记录。
--
-- MySQL 只允许 SIGNAL 出现在存储程序或复合语句里，顶层 IF 不可用，因此用一次性
-- 存储过程做检查：超长值存在时 CALL 以 SQLSTATE 45000 失败，后续 ALTER 不执行。
-- 过程名以 _0023_ 开头，避免与业务对象冲突；首行 DROP IF EXISTS 保证上次失败
-- 残留的过程不会阻止重试。检查通过后过程随即删除。

DROP PROCEDURE IF EXISTS _0023_guard_result_width;
CREATE PROCEDURE _0023_guard_result_width()
BEGIN
  IF EXISTS (
    SELECT 1 FROM t_admin_audit_log WHERE CHAR_LENGTH(result) > 16
  ) THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = '0023 回滚被拒绝：t_admin_audit_log.result 存在超过 16 字符的值，收窄会截断审计记录';
  END IF;
END;
CALL _0023_guard_result_width();
DROP PROCEDURE _0023_guard_result_width;

ALTER TABLE t_admin_audit_log
  MODIFY COLUMN result varchar(16) NOT NULL DEFAULT 'OK'
    COMMENT 'OK / REJECTED_BY_GUARD（B-21 自操作拦截）';
