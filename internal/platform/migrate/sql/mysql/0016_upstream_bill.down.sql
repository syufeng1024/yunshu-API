-- 0016 回滚：删除上游账单表。对账差异记录的处置应在此之前完成。

DROP TABLE IF EXISTS t_upstream_bill;
