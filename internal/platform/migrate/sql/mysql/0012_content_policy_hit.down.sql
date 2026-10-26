-- 0012 回滚：删除命中记录表。本表无下游外键，直接删除即可。

DROP TABLE IF EXISTS t_content_policy_hit;
