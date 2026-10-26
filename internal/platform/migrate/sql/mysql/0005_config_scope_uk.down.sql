-- 0005 回滚：换回两列唯一键。若已存在同键的覆盖行，本语句会因唯一冲突失败，
-- 必须先清理覆盖行再由人工执行，不允许静默删数据。

ALTER TABLE t_config_item
  DROP INDEX uk_cfg_ns_key_scope,
  ADD UNIQUE KEY uk_cfg_ns_key (cfg_namespace, cfg_key);
