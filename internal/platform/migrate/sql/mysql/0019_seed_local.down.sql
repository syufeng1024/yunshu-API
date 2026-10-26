-- 0019 回滚：只删除本地联调种子，精确按名称 / 模型 id 删除，禁止 TRUNCATE。
-- 顺序：价目规则 → 路由 → 别名 → 模型 → 渠道。

DELETE FROM t_price_rule
WHERE scope_level = 'MODEL'
  AND scope_id IN (
    SELECT id FROM t_model
    WHERE model_name IN ('local-qwen-plus', 'local-deepseek-chat')
  );

DELETE FROM t_route
WHERE model_id IN (
    SELECT id FROM t_model
    WHERE model_name IN ('local-qwen-plus', 'local-deepseek-chat')
  )
   OR channel_id IN (
    SELECT id FROM t_channel WHERE name = 'local-domestic-channel'
  );

DELETE FROM t_model_alias
WHERE model_id IN (
  SELECT id FROM t_model
  WHERE model_name IN ('local-qwen-plus', 'local-deepseek-chat')
);

DELETE FROM t_model
WHERE model_name IN ('local-qwen-plus', 'local-deepseek-chat');

DELETE FROM t_channel
WHERE name = 'local-domestic-channel';
