-- 0017 专属渠道 + 模型推荐位（PRD §6.2 F14-6 / §6.7 F22-5，TDS §1.4）。
-- recommend_weight = 0 表示不进推荐位；非 0 仅对 listing_state='LISTED' 的模型有效，
-- 该校验在业务层，DDL 只加列与排序索引。推荐权重只改变展示顺序，不影响路由、计费与限流。
-- dedicated_scope_mode='SELECTED' 时仅名单内用户可见该渠道；fallback_allowed 默认 0（不回落共享池）。

ALTER TABLE t_model
  ADD COLUMN recommend_weight int unsigned NOT NULL DEFAULT 0
    COMMENT 'F22-5 推荐权重，值越大越靠前；仅已上架且已配价的模型可设，转 CATALOG_ONLY / DELISTED 时由后台置 0' AFTER listing_state,
  ADD KEY idx_model_recommend (recommend_weight DESC, published_at DESC)
    COMMENT '对外推荐排序键：权重大者优先，同权重按首次上架时间新者优先';

ALTER TABLE t_channel
  ADD COLUMN dedicated_scope_mode varchar(16) NOT NULL DEFAULT 'ALL'
    COMMENT 'ALL / SELECTED；F14-6 逻辑专属，SELECTED 时仅名单内用户可见该渠道' AFTER group_scope_mode,
  ADD COLUMN fallback_allowed tinyint(1) NOT NULL DEFAULT 0
    COMMENT 'F14-6 边界④：专属渠道全不可用时是否允许回落共享池；默认 0（不回落）' AFTER dedicated_scope_mode;

CREATE TABLE t_channel_dedicated_user (
  id         bigint unsigned NOT NULL AUTO_INCREMENT,
  channel_id bigint unsigned NOT NULL,
  user_id    bigint unsigned NOT NULL,
  created_by bigint unsigned NOT NULL COMMENT '运营操作人；变动须二次确认并留痕',
  created_at datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_cdu_channel_user (channel_id, user_id),
  KEY idx_cdu_user (user_id) COMMENT '选路时按 user_id 反查可用渠道'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
