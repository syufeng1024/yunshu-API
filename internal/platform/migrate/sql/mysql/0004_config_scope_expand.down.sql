-- 0004 回滚：删除作用域列。必须先回滚 0005（唯一键仍引用这两列时不能删）。

ALTER TABLE t_config_item
  DROP COLUMN scope_id,
  DROP COLUMN scope;
