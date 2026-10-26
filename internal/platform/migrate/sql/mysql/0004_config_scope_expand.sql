-- 0004 Expand 第一步：t_config_item 加作用域列，唯一键仍是 (cfg_namespace, cfg_key)。
-- 回填由 NOT NULL DEFAULT 承载，不写显式 UPDATE（避免整表重写）。

ALTER TABLE t_config_item
  ADD COLUMN scope varchar(16) NOT NULL DEFAULT 'GLOBAL'
    COMMENT '作用域：GLOBAL / CHANNEL / USER' AFTER cfg_key,
  ADD COLUMN scope_id bigint unsigned NOT NULL DEFAULT 0
    COMMENT '作用域对象 id；GLOBAL 恒为 0。不用 NULL——NULL 在唯一索引中互不冲突，会让约束失效' AFTER scope;
