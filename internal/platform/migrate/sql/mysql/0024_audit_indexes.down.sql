-- 0024 回滚：删除审计检索新增的两个索引。
--
-- 索引不承载数据，DROP INDEX 不改变任何行，也没有截断或类型收窄风险；
-- 回滚后查询退化为 0024 之前的行为（时间窗口 filesort、action 过滤全表），
-- 结果集不变，只是变慢。

ALTER TABLE t_admin_audit_log
  DROP INDEX idx_aal_action_created,
  DROP INDEX idx_aal_created;
