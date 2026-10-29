-- 0026 回滚：整表删除。5 行是种子数据，没有任何外键或下游引用，
-- 也不存在需要保留的运营配置副本：重新执行 up 会回到全部关闭的初始状态。
DROP TABLE IF EXISTS t_integration;
