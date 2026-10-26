-- 0015 回滚：先删回复（引用工单），再删工单与审核任务。无外键，顺序仅表达逻辑依赖。

DROP TABLE IF EXISTS t_ticket_reply;
DROP TABLE IF EXISTS t_review_task;
DROP TABLE IF EXISTS t_ticket;
