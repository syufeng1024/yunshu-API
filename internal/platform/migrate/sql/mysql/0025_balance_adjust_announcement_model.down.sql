-- 0025 回滚：先删关联表，再删单据表。两表都没有外键，顺序仅按创建逆序。
DROP TABLE IF EXISTS t_announcement_model;
DROP TABLE IF EXISTS t_balance_adjust;
