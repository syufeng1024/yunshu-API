-- 0013 幂等 Contract 第四步：删除旧键 uk_idem。
-- 前置条件（正确性，不是性能）：0010 已应用、读路径已按 api_key_id + body_hash 过滤、
-- 灰度 100% 且稳定 7 天。先删会让代码尚未全量升级的窗口失去跨 Key 保护。
-- 本迁移之后不可回滚，只能前滚；down 文件故意保留为 SIGNAL 失败。
-- 注意：0013 不得先于 0010 在任何环境执行。

ALTER TABLE t_idempotency_record
  DROP INDEX uk_idem;
