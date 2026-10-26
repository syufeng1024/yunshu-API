-- 0009 回滚：只删除本迁移写入的五档，不触碰运营后续新增的套餐行。

DELETE FROM t_plan
WHERE code IN ('TRIAL', 'STANDARD', 'PRO', 'ENTERPRISE', 'FREE_DEFAULT');
