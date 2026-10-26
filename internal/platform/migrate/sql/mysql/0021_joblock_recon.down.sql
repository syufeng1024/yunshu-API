-- 0021 回滚：先删子表 t_reconcile_diff，再删 t_job_lock。
--
-- 两表之间没有外键；顺序按「被依赖的先删」执行，便于以后若补外键时回滚脚本
-- 仍然正确。前置条件：已停止 jobsd，且对账任务不再写入 t_reconcile_diff。

DROP TABLE IF EXISTS t_reconcile_diff;
DROP TABLE IF EXISTS t_job_lock;
