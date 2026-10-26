-- 0002 回滚：按建表倒序删 11 张配置表。
-- 必须在 0004 / 0005 / 0006 的 down 之后执行：t_config_item 的作用域列随 0004 撤销，
-- 0006 的两张分组表不依赖本文件，但 t_api_key.group_code 已在 0006 的 down 中移除。

DROP TABLE IF EXISTS t_config_publish;
DROP TABLE IF EXISTS t_config_runtime;
DROP TABLE IF EXISTS t_config_item;
DROP TABLE IF EXISTS t_plan;
DROP TABLE IF EXISTS t_price_rule;
DROP TABLE IF EXISTS t_route;
DROP TABLE IF EXISTS t_model_capability;
DROP TABLE IF EXISTS t_model_alias;
DROP TABLE IF EXISTS t_model;
DROP TABLE IF EXISTS t_channel;
DROP TABLE IF EXISTS t_protocol;
