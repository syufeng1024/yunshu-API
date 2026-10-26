-- 0001 账号与凭据：8 张表。
-- 全库 utf8mb4_0900_as_cs；时间列 datetime(3) 存 UTC；不建外键。

CREATE TABLE t_user (
  id                    bigint unsigned NOT NULL AUTO_INCREMENT,
  user_no               char(12)        NOT NULL COMMENT '对外展示号 U+yyyymmdd+4位序号',
  email                 varchar(254)    DEFAULT NULL COMMENT 'RFC5321 上限 254',
  phone                 varchar(20)     DEFAULT NULL COMMENT 'E.164 去 + 号，如 8613800138000',
  password_hash         varchar(120)    NOT NULL COMMENT 'bcrypt cost=12 输出 60 字符，留 120 兼容 argon2id',
  password_algo         varchar(16)     NOT NULL DEFAULT 'bcrypt' COMMENT 'bcrypt / argon2id；算法升级时按行判定',
  password_updated_at   datetime(3)     NOT NULL,
  nickname              varchar(64)     NOT NULL DEFAULT '',
  role                  varchar(16)     NOT NULL DEFAULT 'PERSONAL' COMMENT 'PERSONAL / OPS / FINANCE / SUPER_ADMIN（B-21 财务运营分离）',
  status                varchar(16)     NOT NULL DEFAULT 'NORMAL' COMMENT 'NORMAL / DISABLED / FROZEN_OVERDRAFT（B-31 统一账号状态）',
  disable_source        varchar(16)     DEFAULT NULL COMMENT 'RISK_AUTO / OPS_MANUAL；仅 status=DISABLED 时有值',
  disable_reason        varchar(255)    DEFAULT NULL,
  disabled_at           datetime(3)     DEFAULT NULL,
  disabled_by           bigint unsigned DEFAULT NULL COMMENT '运营手动禁用时的操作人；RISK_AUTO 为 NULL',
  risk_score            smallint        NOT NULL DEFAULT 0 COMMENT '0~100，风控累计分',
  plan_id               bigint unsigned DEFAULT NULL COMMENT '当前生效套餐；NULL = 免费默认档',
  plan_purchased_at     datetime(3)     DEFAULT NULL,
  verify_status         varchar(16)     NOT NULL DEFAULT 'UNVERIFIED' COMMENT 'UNVERIFIED / PENDING / VERIFIED / FAILED',
  verified_at           datetime(3)     DEFAULT NULL,
  trial_claimed_at      datetime(3)     DEFAULT NULL COMMENT '体验积分领取时点（B-29 主动领取）',
  trial_expire_at       datetime(3)     DEFAULT NULL COMMENT '注册起 7 天，到期回落免费默认档',
  full_log_enabled      tinyint(1)      NOT NULL DEFAULT 0 COMMENT '完整日志开关（B-67 per-User 一级）',
  overdraft_limit_micro bigint          NOT NULL DEFAULT 500000 COMMENT '透支上限，默认 50 积分（B-05 A 档）',
  frozen_until          datetime(3)     DEFAULT NULL COMMENT '注销冷静期冻结截止（B-27）',
  cancel_apply_at       datetime(3)     DEFAULT NULL COMMENT '注销申请时点（B-27）',
  agreement_version     varchar(16)     NOT NULL DEFAULT '' COMMENT '最后勾选的用户协议版本（B-61）',
  agreed_at             datetime(3)     DEFAULT NULL,
  team_type             varchar(16)     NOT NULL DEFAULT 'PERSONAL' COMMENT '预留，V1.0 恒 PERSONAL（B-16）',
  org_name              varchar(128)    DEFAULT NULL COMMENT '预留，V1.0 不展示（B-16）',
  verified_id_no_hmac   char(64)        DEFAULT NULL COMMENT 'HMAC-SHA256(身份证号, ID_HMAC_KEY)；用于唯一性与防重，不可逆',
  verified_name_enc     varbinary(512)  DEFAULT NULL COMMENT '信封加密后的姓名，KMS 数据密钥加密',
  verify_provider       varchar(32)     DEFAULT NULL COMMENT '二要素核身渠道标识',
  created_at            datetime(3)     NOT NULL,
  updated_at            datetime(3)     NOT NULL,
  deleted_at            datetime(3)     DEFAULT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_no (user_no),
  UNIQUE KEY uk_user_email (email),
  UNIQUE KEY uk_user_phone (phone),
  UNIQUE KEY uk_user_idno (verified_id_no_hmac),
  KEY idx_user_status_created (status, created_at),
  KEY idx_user_plan (plan_id),
  KEY idx_user_trial_expire (trial_expire_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_verify_record (
  id             bigint unsigned NOT NULL AUTO_INCREMENT,
  user_id        bigint unsigned NOT NULL,
  provider       varchar(32)     NOT NULL COMMENT '核身渠道标识',
  provider_txn   varchar(64)     DEFAULT NULL COMMENT '上游核身流水号，对账用',
  id_no_hmac     char(64)        NOT NULL,
  result         varchar(16)     NOT NULL COMMENT 'MATCH / MISMATCH / LOCKED / SYSTEM_ERROR',
  fail_reason    varchar(128)    DEFAULT NULL,
  client_ip      varbinary(16)   DEFAULT NULL COMMENT 'INET6_ATON 后的二进制，varchar 存 IP 无法做网段计算',
  created_at     datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  KEY idx_vr_user_created (user_id, created_at),
  KEY idx_vr_provider_txn (provider_txn)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_device_fingerprint (
  id              bigint unsigned NOT NULL AUTO_INCREMENT,
  fp_hash         char(64)        NOT NULL COMMENT 'HMAC-SHA256(设备指纹原始串, FP_HMAC_KEY)',
  user_id         bigint unsigned NOT NULL,
  first_seen_at   datetime(3)     NOT NULL,
  last_seen_at    datetime(3)     NOT NULL,
  seen_count      int unsigned    NOT NULL DEFAULT 1,
  trial_granted   tinyint(1)      NOT NULL DEFAULT 0 COMMENT '该指纹下是否已发放过体验积分',
  granted_user_id bigint unsigned DEFAULT NULL COMMENT '首个领取体验积分的账号',
  created_at      datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_fp_hash_user (fp_hash, user_id),
  KEY idx_fp_hash_granted (fp_hash, trial_granted)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_verification_code (
  id            bigint unsigned NOT NULL AUTO_INCREMENT,
  target        varchar(254)    NOT NULL COMMENT '邮箱或手机号，明文存以便频控与重发',
  scene         varchar(24)     NOT NULL COMMENT 'REGISTER / LOGIN / RESET_PWD / REBIND_OLD / REBIND_NEW / INVOICE',
  code_hash     char(64)        NOT NULL COMMENT 'SHA256(code + scene + target + CODE_PEPPER)',
  code_algo     varchar(16)     NOT NULL DEFAULT 'sha256' COMMENT 'T-32 算法版本列，便于后续换 argon2',
  attempt_count tinyint unsigned NOT NULL DEFAULT 0 COMMENT '校验失败次数，>= 5 作废',
  consumed_at   datetime(3)     DEFAULT NULL,
  expires_at    datetime(3)     NOT NULL COMMENT '签发 + 10 分钟',
  send_ip       varbinary(16)   DEFAULT NULL,
  created_at    datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  KEY idx_vc_target_scene (target, scene, created_at) COMMENT '频控：60 秒内同 target+scene 只发一条',
  KEY idx_vc_expire (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_api_key (
  id                bigint unsigned NOT NULL AUTO_INCREMENT,
  user_id           bigint unsigned NOT NULL,
  key_name          varchar(64)     NOT NULL,
  key_prefix        char(8)         NOT NULL COMMENT '明文前 8 位，列表展示用',
  key_hash          char(64)        NOT NULL COMMENT 'SHA256(完整 key)，鉴权等值查找',
  key_hash_version  smallint        NOT NULL DEFAULT 1 COMMENT 'T-32 哈希算法版本',
  key_last4         char(4)         NOT NULL COMMENT '掩码展示尾部 4 位',
  status            varchar(16)     NOT NULL DEFAULT 'ENABLED' COMMENT 'ENABLED / DISABLED',
  model_scope_mode  varchar(16)     NOT NULL DEFAULT 'SELECTED' COMMENT 'SELECTED（显式勾选，B-35 必选）/ ALL',
  ip_whitelist_mode varchar(16)     NOT NULL DEFAULT 'OFF' COMMENT 'OFF（留空=不限制，B-35）/ ON',
  expire_at         datetime(3)     DEFAULT NULL COMMENT 'NULL = 永久',
  last_used_at      datetime(3)     DEFAULT NULL,
  rotated_from_id   bigint unsigned DEFAULT NULL COMMENT 'B-40 重新生成时指向旧 Key',
  revoked_at        datetime(3)     DEFAULT NULL COMMENT '重新生成后旧 Key 的失效时点',
  deleted_at        datetime(3)     DEFAULT NULL,
  created_at        datetime(3)     NOT NULL,
  updated_at        datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_key_hash (key_hash),
  KEY idx_key_user_status (user_id, status),
  KEY idx_key_user_deleted (user_id, deleted_at),
  KEY idx_key_rotated (rotated_from_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_api_key_model (
  id          bigint unsigned NOT NULL AUTO_INCREMENT,
  api_key_id  bigint unsigned NOT NULL,
  model_id    bigint unsigned NOT NULL,
  created_at  datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_akm_key_model (api_key_id, model_id),
  KEY idx_akm_model (model_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_api_key_ip_rule (
  id          bigint unsigned NOT NULL AUTO_INCREMENT,
  api_key_id  bigint unsigned NOT NULL,
  cidr        varchar(43)     NOT NULL COMMENT '单 IP 或 CIDR；v4 最长 15+3，v6 最长 39+4',
  ip_family   tinyint unsigned NOT NULL COMMENT '4 / 6',
  note        varchar(64)     DEFAULT NULL,
  created_at  datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_akip_key_cidr (api_key_id, cidr),
  KEY idx_akip_key (api_key_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

CREATE TABLE t_api_key_audit (
  id          bigint unsigned NOT NULL AUTO_INCREMENT,
  api_key_id  bigint unsigned NOT NULL,
  action      varchar(24)     NOT NULL COMMENT 'CREATE / ROTATE / DISABLE / ENABLE / DELETE / MODEL_SCOPE_CHANGE / IP_RULE_CHANGE',
  before_json json            DEFAULT NULL COMMENT '变更前（T-18 脱敏后）',
  after_json  json            DEFAULT NULL,
  operator_id bigint unsigned NOT NULL,
  operator_ip varbinary(16)   DEFAULT NULL,
  created_at  datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  KEY idx_aka_key_created (api_key_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;
