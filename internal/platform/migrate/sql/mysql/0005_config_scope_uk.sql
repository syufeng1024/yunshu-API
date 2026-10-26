-- 0005 Contract：唯一键换成四列，允许同键的全局行与覆盖行并存。
-- 新键严格弱于旧键：旧代码不写 scope / scope_id 时取默认值，新键退化为旧键。

ALTER TABLE t_config_item
  DROP INDEX uk_cfg_ns_key,
  ADD UNIQUE KEY uk_cfg_ns_key_scope (cfg_namespace, cfg_key, scope, scope_id);
