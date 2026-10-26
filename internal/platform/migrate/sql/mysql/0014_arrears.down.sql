-- 0014 回滚：删除欠费登记表。欠费是账本缺口的外部记录，回滚前须确认没有未结清欠费。

DROP TABLE IF EXISTS t_arrears;
