-- 0006 回滚：先撤 t_api_key 的列与索引，再删两张分组表。

ALTER TABLE t_api_key
  DROP INDEX idx_key_group,
  DROP COLUMN group_code;

DROP TABLE IF EXISTS t_price_group_scope;
DROP TABLE IF EXISTS t_price_group;
