-- 0024 给 t_admin_audit_log 补两个索引，修复审计检索的索引缺口。
--
-- 0003 只建了三个复合索引：
--   idx_aal_operator_created (operator_id, created_at)
--   idx_aal_target           (target_type, target_id, created_at)
--   idx_aal_module_created   (module, created_at)
-- 查询窗口永远带 created_at 范围，但没有任何索引以 created_at 为前导列：
-- 只按时间翻页的请求只能扫主键后 filesort，只按 action 过滤的请求同样
-- 没有可用的等值前缀。运营后台要按动作筛选（如只看 ADJUST_BALANCE），
-- 不能靠限制调用方式回避。
--
-- idx_aal_created 服务时间窗口 + 倒序分页；id 作为第二列让
-- ORDER BY created_at DESC, id DESC 走同一个索引，不再依赖「id 单调等于
-- 时间序」的假设（补录会让这个假设失效）。
-- idx_aal_action_created 服务 action 等值 + 时间倒序。
--
-- 两个索引都只加列、不改行；表是 append-only，建索引期间无 UPDATE 冲突。

ALTER TABLE t_admin_audit_log
  ADD KEY idx_aal_created (created_at, id),
  ADD KEY idx_aal_action_created (action, created_at, id);
