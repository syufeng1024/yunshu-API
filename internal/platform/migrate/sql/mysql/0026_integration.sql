-- 0026 工具集成深链模板（T33 段 1 / D-30）。
--
-- 本表只存「深链模板」，不存任何凭据：D-30 Q1 定稿 V1.0 不做
-- POST /api/v1/integrations/import-credentials，平台不代收用户上游 Key。
-- 前端按模板渲染深链，参数名由后端持有（F4-3），浏览器只看到渲染结果。
--
-- tool_key 与 web/src/content/toolIntegrations.ts 的 key 逐字一致。5 行是
-- 产品常量（PRD §4.5 / D-30 Q3），运营只能改 enabled 与 deep_link_template，
-- 不能增删。默认 enabled=0 且模板为 NULL：新增工具不会在运营配置前把用户
-- 带到错误地址。
--
-- 无软删列、无外键：5 行不会被删除，唯一键 uk_integration_tool 阻止重复种子。
-- deep_link_template 上限 512 字符，服务端另做占位符白名单校验
-- （{key} / {base_url} / {model}），不是只靠列宽兜底。

CREATE TABLE t_integration (
  id                 bigint unsigned NOT NULL AUTO_INCREMENT,
  tool_key           varchar(64)     NOT NULL COMMENT '与 toolIntegrations.ts 的 key 逐字一致',
  display_name       varchar(64)     NOT NULL,
  enabled            tinyint(1)      NOT NULL DEFAULT 0,
  deep_link_template varchar(512)    DEFAULT NULL COMMENT '仅允许 {key}/{base_url}/{model} 占位符',
  created_at         datetime(3)     NOT NULL,
  updated_at         datetime(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_integration_tool (tool_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_as_cs;

INSERT INTO t_integration (tool_key, display_name, enabled, deep_link_template, created_at, updated_at) VALUES
  ('cursor',        'Cursor',        0, NULL, NOW(3), NOW(3)),
  ('claude-code',   'Claude Code',   0, NULL, NOW(3), NOW(3)),
  ('cherry-studio', 'Cherry Studio', 0, NULL, NOW(3), NOW(3)),
  ('nextchat',      'NextChat',      0, NULL, NOW(3), NOW(3)),
  ('openai-sdk',    'OpenAI SDK',    0, NULL, NOW(3), NOW(3));
