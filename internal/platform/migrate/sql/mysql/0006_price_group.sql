-- 0006 价格分组：两张新表 + t_api_key.group_code。
-- 分组倍率是价格维度，不是权限维度；完整性由控制面校验，不建外键。

CREATE TABLE t_price_group (
  id                 bigint unsigned NOT NULL AUTO_INCREMENT,
  code               varchar(24)     NOT NULL COMMENT 'standard / contract / internal；对外接口与 K13 快照均用 code，不用 id',
  name               varchar(64)     NOT NULL,
  ratio_bp           int unsigned    NOT NULL DEFAULT 10000 COMMENT '价格倍率，万分比：10000=1.0，8500=0.85，0=免费。约束 0<=x<=10000（需求 §3.5 不设 >1）',
  model_scope_mode   varchar(16)     NOT NULL DEFAULT 'ALL' COMMENT 'ALL / SELECTED（SELECTED 时以 t_price_group_scope.scope_type=MODEL 为准）',
  channel_scope_mode varchar(16)     NOT NULL DEFAULT 'ALL' COMMENT 'ALL / SELECTED；风险隔离执行点：受限分组只走非主力渠道',
  self_service       tinyint(1)      NOT NULL DEFAULT 0 COMMENT '是否允许用户自助切到本分组；V1.0 全 0（需求 §3.5 禁止向下自助切换）',
  min_margin_bp      smallint unsigned DEFAULT NULL COMMENT '本分组覆盖全局毛利地板；NULL 时取 pricing.min_margin_bp',
  remark             varchar(255)    NOT NULL DEFAULT '',
  enabled            tinyint(1)      NOT NULL DEFAULT 1,
  created_by         bigint unsigned NOT NULL,
  created_at         datetime(3)     NOT NULL,
  updated_at         datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_pg_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_price_group_scope (
  id         bigint unsigned NOT NULL AUTO_INCREMENT,
  group_id   bigint unsigned NOT NULL,
  scope_type varchar(16)     NOT NULL COMMENT 'MODEL / CHANNEL',
  scope_id   bigint unsigned NOT NULL COMMENT '指向 t_model.id 或 t_channel.id',
  created_at datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_pgs_group_scope (group_id, scope_type, scope_id),
  KEY idx_pgs_scope (scope_type, scope_id) COMMENT '反向查询：某渠道被哪些分组可见'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

ALTER TABLE t_api_key
  ADD COLUMN group_code varchar(32) NOT NULL DEFAULT 'standard'
    COMMENT '价格分组（需求 §3.5）；指向 t_price_group.code。存 code 而非 id：网关鉴权快照直接带 code 与倍率，P09 取价不需回表；改分组只改一行'
    AFTER model_scope_mode,
  ADD KEY idx_key_group (group_code, status) COMMENT '后台按分组统计与批量调组';
