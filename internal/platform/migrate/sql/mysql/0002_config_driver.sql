-- 0002 配置驱动层：11 张表。不含 t_config_item.scope / scope_id（0004 扩展）与价格分组（0006）。
-- t_model 直接以 listing_state 三态建表；0007 / 0008 永久预留，不创建。

CREATE TABLE t_protocol (
  id                bigint unsigned NOT NULL AUTO_INCREMENT,
  code              varchar(32)     NOT NULL COMMENT 'BAILIAN / OPENAI_COMPAT / ANTHROPIC_COMPAT / CUSTOM',
  name              varchar(64)     NOT NULL,
  direction         varchar(16)     NOT NULL DEFAULT 'BOTH' COMMENT 'INBOUND（对外暴露）/ OUTBOUND（上游接入）/ BOTH',
  adapter_version   varchar(16)     NOT NULL DEFAULT 'v1' COMMENT 'T-15 适配器契约版本，用于契约测试矩阵',
  stream_usage_mode varchar(24)     NOT NULL DEFAULT 'UNKNOWN' COMMENT 'NATIVE / STREAM_OPTIONS / ESTIMATE / UNKNOWN——见 §3.4',
  body_parser       varchar(24)     NOT NULL DEFAULT 'IR_JSON' COMMENT 'IR_JSON（两段式转换，D-11）',
  enabled           tinyint(1)      NOT NULL DEFAULT 1,
  created_at        datetime(3)     NOT NULL,
  updated_at        datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_proto_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_channel (
  id                    bigint unsigned NOT NULL AUTO_INCREMENT,
  name                  varchar(64)     NOT NULL,
  protocol_id           bigint unsigned NOT NULL,
  region                varchar(16)     NOT NULL COMMENT 'DOMESTIC / OVERSEAS；V1.0 强制 DOMESTIC（§3.4）',
  base_url              varchar(512)    NOT NULL COMMENT 'https 强制；http 仅私网地址放行',
  upstream_key_enc      varbinary(1024) NOT NULL COMMENT '信封加密：DEK 加密值 + DEK 密文（KMS）',
  upstream_key_mask     varchar(32)     NOT NULL COMMENT '全掩码值，如 sk-****abcd；B-14 / §5.7 要求全部掩码',
  key_rotation_at       datetime(3)     DEFAULT NULL,
  supported_models      json            DEFAULT NULL COMMENT '冗余快照，仅供后台展示；路由事实来源为 t_route（T-34）',
  group_scope_mode      varchar(16)     NOT NULL DEFAULT 'ALL' COMMENT 'ALL（全部分组可见）/ SELECTED（以 t_price_group_scope.scope_type=CHANNEL 为准）',
  weight                int unsigned    NOT NULL DEFAULT 100 COMMENT '同模型多路由时的权重分母',
  priority              smallint        NOT NULL DEFAULT 100 COMMENT 'T-34：权重列整数型，数值越小优先级越高，同优先级内按权重随机',
  timeout_connect_ms    int unsigned    NOT NULL DEFAULT 3000,
  timeout_first_byte_ms int unsigned    NOT NULL DEFAULT 60000 COMMENT 'B-76 默认 60s',
  timeout_total_ms      int unsigned    NOT NULL DEFAULT 120000 COMMENT 'B-76 默认 120s',
  retry_max             tinyint unsigned NOT NULL DEFAULT 2 COMMENT 'T-16 渠道级重试次数，0~2',
  stream_supported      tinyint(1)      NOT NULL DEFAULT 1,
  stream_usage_mode     varchar(24)     NOT NULL DEFAULT 'UNKNOWN' COMMENT '渠道级覆盖协议级；见 §3.4',
  upstream_model_name_map json          DEFAULT NULL,
  health_state          varchar(16)     NOT NULL DEFAULT 'CLOSED' COMMENT 'CLOSED / OPEN / HALF_OPEN（熔断态，运行态主源在 Redis）',
  enabled               tinyint(1)      NOT NULL DEFAULT 1,
  created_by            bigint unsigned NOT NULL,
  updated_by            bigint unsigned DEFAULT NULL,
  created_at            datetime(3)     NOT NULL,
  updated_at            datetime(3)     NOT NULL,
  deleted_at            datetime(3)     DEFAULT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_channel_name (name),
  KEY idx_channel_proto_enabled (protocol_id, enabled),
  KEY idx_channel_region_enabled (region, enabled) COMMENT 'T-13 三层校验之二：选路 SQL 强制 region=DOMESTIC'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_model (
  id                    bigint unsigned NOT NULL AUTO_INCREMENT,
  model_name            varchar(128)    NOT NULL COMMENT '对外模型名，客户端唯一可见标识',
  display_name          varchar(128)    NOT NULL,
  region                enum('DOMESTIC','OVERSEAS') NOT NULL COMMENT 'V1.0 强制 DOMESTIC',
  model_type            varchar(16)     NOT NULL COMMENT 'TEXT / IMAGE / EMBEDDING（B-07 向量归 EMBEDDING）',
  protocol_id           bigint unsigned NOT NULL COMMENT '默认协议，路由未显式指定时的回落值',
  primary_channel_id    bigint unsigned DEFAULT NULL,
  context_length        int unsigned    NOT NULL DEFAULT 0,
  capability_mask       smallint unsigned NOT NULL DEFAULT 0 COMMENT 'T-31 位掩码：bit0 STREAM / bit1 TOOL_CALL / bit2 VISION / bit3 JSON_MODE',
  default_price_rule_id bigint unsigned DEFAULT NULL COMMENT '唯一价格写源为 t_price_rule，本列仅为快捷指针',
  listing_state         enum('LISTED','CATALOG_ONLY','DELISTED') NOT NULL DEFAULT 'LISTED' COMMENT 'LISTED（对外可见可路由）/ CATALOG_ONLY（仅目录）/ DELISTED（已下架）',
  published_at          datetime(3)     DEFAULT NULL COMMENT '首次上架时间；CATALOG_ONLY 期间不清空',
  delisted_at           datetime(3)     DEFAULT NULL COMMENT '置为 DELISTED 的时点；仅运营可见',
  created_at            datetime(3)     NOT NULL,
  updated_at            datetime(3)     NOT NULL,
  deleted_at            datetime(3)     DEFAULT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_model_name (model_name) COMMENT 'utf8mb4_0900_as_cs，qwen-Max != qwen-max（T-33）',
  KEY idx_model_type_state (model_type, listing_state),
  KEY idx_model_region_state (region, listing_state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_model_alias (
  id          bigint unsigned NOT NULL AUTO_INCREMENT,
  alias_name  varchar(128)    NOT NULL COMMENT '客户端可传入的别名，如 deepseek-chat',
  model_id    bigint unsigned NOT NULL,
  source      varchar(16)     NOT NULL DEFAULT 'OPS' COMMENT 'OPS（运营配置）/ AUTO（上游官方名自动收集）',
  enabled     tinyint(1)      NOT NULL DEFAULT 1,
  created_at  datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_alias_name (alias_name),
  KEY idx_alias_model (model_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_model_capability (
  id          bigint unsigned NOT NULL AUTO_INCREMENT,
  model_id    bigint unsigned NOT NULL,
  cap_code    varchar(24)     NOT NULL COMMENT 'STREAM / TOOL_CALL / VISION / JSON_MODE',
  supported   tinyint(1)      NOT NULL COMMENT 'B-53：未配置行显示「—」，配置 supported=0 显示「不支持」，两者语义不同',
  note        varchar(128)    DEFAULT NULL,
  updated_at  datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_mc_model_cap (model_id, cap_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_route (
  id                  bigint unsigned NOT NULL AUTO_INCREMENT,
  model_id            bigint unsigned NOT NULL,
  channel_id          bigint unsigned NOT NULL,
  protocol_id         bigint unsigned NOT NULL COMMENT '可覆盖渠道默认协议（同渠道多协议接入）',
  upstream_model_name varchar(128)    NOT NULL COMMENT '真实上游模型名，对客户端不可见',
  region              varchar(16)     NOT NULL COMMENT '冗余自 channel.region，选路时免 JOIN（T-34）',
  weight              int unsigned    NOT NULL DEFAULT 100 COMMENT '0 = 不参与分流（灰度挂起）',
  priority            smallint        NOT NULL DEFAULT 100,
  stream_supported    tinyint(1)      NOT NULL DEFAULT 1 COMMENT 'false 时流式请求跳过该路由',
  max_tokens_ceiling  int unsigned    NOT NULL DEFAULT 0 COMMENT '0 = 不限；上游模型硬上限，超出返回 max_tokens_exceeded',
  enabled             tinyint(1)      NOT NULL DEFAULT 1,
  probe_at            datetime(3)     DEFAULT NULL COMMENT '最近一次连通性探测',
  probe_result        varchar(16)     DEFAULT NULL COMMENT 'OK / AUTH_FAIL / TIMEOUT / HTTP_5XX / DNS_FAIL',
  created_at          datetime(3)     NOT NULL,
  updated_at          datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_route_model_channel_proto_upstream (model_id, channel_id, protocol_id, upstream_model_name),
  KEY idx_route_model_enabled (model_id, enabled, region, priority) COMMENT '选路主索引：model_id + enabled + region + priority',
  KEY idx_route_channel (channel_id) COMMENT '渠道删除前置校验：存在引用则拒绝'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_price_rule (
  id                             bigint unsigned NOT NULL AUTO_INCREMENT,
  scope_level                    varchar(16)     NOT NULL COMMENT 'MODEL / PROTOCOL / CHANNEL（B-19：PROTOCOL 层 V1.0 恒为空）',
  scope_id                       bigint unsigned NOT NULL COMMENT '按 scope_level 指向 t_model.id / t_protocol.id / t_channel.id',
  model_type                     varchar(16)     NOT NULL COMMENT 'TEXT / IMAGE / EMBEDDING，决定计价公式（B-07）',
  in_price_micro_per_k           int unsigned    NOT NULL DEFAULT 0 COMMENT '输入单价，微积分/千token；IMAGE 类型恒 0',
  out_price_micro_per_k          int unsigned    NOT NULL DEFAULT 0 COMMENT '输出单价；IMAGE / EMBEDDING 类型恒 0（B-07 向量无输出价）',
  per_call_price_micro           bigint unsigned NOT NULL DEFAULT 0 COMMENT 'IMAGE 按张单价，微积分/张',
  cache_hit_ratio_bp             smallint unsigned DEFAULT NULL COMMENT '缓存命中 token 计价倍率，万分比（1000 = 输入价的 0.1）。NULL = 该模型不计缓存折扣',
  cache_write_ratio_bp           smallint unsigned DEFAULT NULL COMMENT '缓存写入 token 计价倍率，万分比（12500 = 输入价的 1.25）。NULL = 上游不收缓存写入费',
  long_ctx_threshold_tokens      int unsigned    DEFAULT NULL COMMENT '长上下文阶梯阈值（输入 token 数）。NULL = 无阶梯',
  long_ctx_in_price_micro_per_k  int unsigned    DEFAULT NULL COMMENT '超阈值档输入单价；NULL 时回落 in_price_micro_per_k',
  long_ctx_out_price_micro_per_k int unsigned    DEFAULT NULL COMMENT '超阈值档输出单价；NULL 时回落 out_price_micro_per_k',
  cost_in_micro_per_k            int unsigned    DEFAULT NULL COMMENT '上游成本价，仅运营可见（B-48）；NULL = 未配置成本价',
  cost_out_micro_per_k           int unsigned    DEFAULT NULL,
  cost_per_call_micro            bigint unsigned DEFAULT NULL,
  currency_cost                  varchar(8)      NOT NULL DEFAULT 'CNY',
  markup_factor_bp               int unsigned    NOT NULL DEFAULT 10000 COMMENT 'B-19 A 档：代理加价维度预留于渠道级，V1.0 恒 10000（万分比 1.0000）且后台无入口',
  version                        int unsigned    NOT NULL DEFAULT 1 COMMENT '价格版本号，每次变更 +1（T-14）',
  effective_from                 datetime(3)     NOT NULL COMMENT '生效时点；B-66 涨价公示期 >= 7 自然日后方可设置',
  effective_to                   datetime(3)     DEFAULT NULL COMMENT 'NULL = 长期有效；被新版本顶替时回填',
  enabled                        tinyint(1)      NOT NULL DEFAULT 1,
  created_by                     bigint unsigned NOT NULL,
  created_at                     datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  KEY idx_price_scope_effective (scope_level, scope_id, enabled, effective_from) COMMENT '定价解析主索引',
  KEY idx_price_effective_from (effective_from) COMMENT 'B-66 公示期校验：生效时点 >= now()+7d'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_plan (
  id                   bigint unsigned NOT NULL AUTO_INCREMENT,
  code                 varchar(24)     NOT NULL COMMENT 'TRIAL / STANDARD / PRO / ENTERPRISE / FREE_DEFAULT',
  name                 varchar(64)     NOT NULL,
  price_fen            int unsigned    NOT NULL DEFAULT 0 COMMENT '售价，单位分；一次性购买',
  grant_recharge_micro bigint unsigned NOT NULL DEFAULT 0 COMMENT 'B-12 A 档：支付 X 元得 Y 充值积分（Y>=X 保底），单位微积分',
  grant_bonus_micro    bigint unsigned NOT NULL DEFAULT 0 COMMENT '赠送积分，不可退，90 天有效（B-02）',
  bonus_valid_days     smallint unsigned NOT NULL DEFAULT 90,
  rpm                  int unsigned    NOT NULL DEFAULT 60,
  tpm                  int unsigned    NOT NULL DEFAULT 60000,
  concurrency          smallint unsigned NOT NULL DEFAULT 5 COMMENT '名义并发上限，实际 per-Key 预置值 = 本值 / 副本数（D-09）',
  daily_quota_micro    bigint unsigned DEFAULT NULL COMMENT '账户日额度，微积分；0 = 继承上一档，NULL = 无额度上限（FREE_DEFAULT，PRD §3.2.1）',
  model_scope_mode     varchar(16)     NOT NULL DEFAULT 'ALL' COMMENT 'B-13 A 档：V1.0 全部套餐恒 ALL',
  invoice_supported    tinyint(1)      NOT NULL DEFAULT 0,
  sla_text             varchar(255)    NOT NULL DEFAULT '' COMMENT 'B-18 A 档：7x24 工单受理，不承诺可用性百分比',
  permanent            tinyint(1)      NOT NULL DEFAULT 1 COMMENT 'B-11 A 档：限流规格一次性购买永久有效',
  stackable            tinyint(1)      NOT NULL DEFAULT 0 COMMENT '同档不可重复购买；跨档高覆盖低',
  sort_order           smallint        NOT NULL DEFAULT 0,
  enabled              tinyint(1)      NOT NULL DEFAULT 1,
  created_at           datetime(3)     NOT NULL,
  updated_at           datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_plan_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_config_item (
  id            bigint unsigned NOT NULL AUTO_INCREMENT,
  cfg_namespace varchar(32)     NOT NULL COMMENT 'ROUTING / RATELIMIT / PRICING / UPSTREAM / FEATURE',
  cfg_key       varchar(64)     NOT NULL,
  cfg_value     json            NOT NULL,
  version       bigint unsigned NOT NULL COMMENT 'D-10 配置版本号，全局单调递增',
  enabled       tinyint(1)      NOT NULL DEFAULT 1,
  updated_by    bigint unsigned DEFAULT NULL,
  created_at    datetime(3)     NOT NULL,
  updated_at    datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_cfg_ns_key (cfg_namespace, cfg_key),
  KEY idx_cfg_version (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_config_runtime (
  id            bigint unsigned NOT NULL AUTO_INCREMENT,
  runtime_key   varchar(64)     NOT NULL COMMENT '如 gateway.replica.count / ratelimit.degrade.mode',
  runtime_value json            NOT NULL,
  version       bigint unsigned NOT NULL,
  updated_at    datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_cr_runtime_key (runtime_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_config_publish (
  id           bigint unsigned NOT NULL AUTO_INCREMENT,
  publish_id   char(26)        NOT NULL COMMENT 'ULID，Pub/Sub 消息体主键，节点用于去重',
  version      bigint unsigned NOT NULL COMMENT '发布后的全局版本号',
  scope        varchar(32)     NOT NULL COMMENT 'ALL / ROUTING / RATELIMIT / PRICING / NODE_SET',
  changed_keys json            NOT NULL,
  operator_id  bigint unsigned DEFAULT NULL,
  created_at   datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_cp_publish_id (publish_id),
  KEY idx_cp_version (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
