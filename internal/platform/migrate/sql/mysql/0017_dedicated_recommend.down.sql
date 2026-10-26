-- 0017 回滚：先删名单表，再撤渠道与模型列 / 索引。

DROP TABLE IF EXISTS t_channel_dedicated_user;

ALTER TABLE t_channel
  DROP COLUMN fallback_allowed,
  DROP COLUMN dedicated_scope_mode;

ALTER TABLE t_model
  DROP INDEX idx_model_recommend,
  DROP COLUMN recommend_weight;
