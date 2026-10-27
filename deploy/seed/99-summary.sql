-- ============================================================================
--  云枢 API 中转站 · 演示数据播种 99：对账摘要 + 账本不变量自检
-- ============================================================================
--  调用方：scripts/seed-demo.sh（每次播种结束都跑一遍）
--  前置变量：@uid（演示账号 t_user.id）
--
--  第 1 段是「数据清单」，第 2 段是账本断言（对应
--  internal/ledger/invariant_test.go 的四条不变量 + 一条用量对齐断言）。
--  两段都是纯只读查询，可以随时单独执行。
-- ============================================================================

SET NAMES utf8mb4 COLLATE utf8mb4_0900_as_cs;

-- ---------------------------------------------------------------------------
-- 1. 数据清单
-- ---------------------------------------------------------------------------
SELECT '余额（积分）' AS metric,
       CAST(FORMAT(a.balance_micro / 10000, 4) AS CHAR) AS value
  FROM t_credit_account a WHERE a.user_id = @uid
UNION ALL
SELECT '  其中充值余额（积分）', CAST(FORMAT(a.balance_recharge_micro / 10000, 4) AS CHAR)
  FROM t_credit_account a WHERE a.user_id = @uid
UNION ALL
SELECT '  其中赠送余额（积分）', CAST(FORMAT(a.balance_bonus_micro / 10000, 4) AS CHAR)
  FROM t_credit_account a WHERE a.user_id = @uid
UNION ALL
SELECT '累计消耗（积分）', CAST(FORMAT(a.total_consumed_micro / 10000, 4) AS CHAR)
  FROM t_credit_account a WHERE a.user_id = @uid
UNION ALL
SELECT '积分批次数', CAST(COUNT(*) AS CHAR)
  FROM t_credit_batch b WHERE b.user_id = @uid
UNION ALL
SELECT '流水条数', CAST(COUNT(*) AS CHAR)
  FROM t_ledger_entry l WHERE l.user_id = @uid
UNION ALL
SELECT '用量行数（含失败）', CAST(COUNT(*) AS CHAR)
  FROM t_usage_record u WHERE u.user_id = @uid
UNION ALL
SELECT '  其中失败行数', CAST(COUNT(*) AS CHAR)
  FROM t_usage_record u WHERE u.user_id = @uid AND u.result = 'FAILED'
UNION ALL
SELECT '上游尝试行数', CAST(COUNT(*) AS CHAR)
  FROM t_upstream_attempt t WHERE t.user_id = @uid
UNION ALL
SELECT '在售模型数（LISTED）', CAST(COUNT(*) AS CHAR)
  FROM t_model m WHERE m.listing_state = 'LISTED' AND m.deleted_at IS NULL
UNION ALL
SELECT '当前生效价目数', CAST(COUNT(*) AS CHAR)
  FROM t_price_rule r
  JOIN t_model m ON m.id = r.scope_id AND r.scope_level = 'MODEL'
 WHERE m.listing_state = 'LISTED' AND m.deleted_at IS NULL
   AND r.enabled = 1 AND r.effective_from <= UTC_TIMESTAMP(3)
   AND (r.effective_to IS NULL OR r.effective_to > UTC_TIMESTAMP(3))
UNION ALL
SELECT 'API Key 数（未删除）', CAST(COUNT(*) AS CHAR)
  FROM t_api_key k WHERE k.user_id = @uid AND k.deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- 2. 账本不变量断言
-- ---------------------------------------------------------------------------
SELECT 'A1' AS `#`,
       'balance = recharge + bonus' AS assertion,
       CAST(CONCAT(a.balance_micro, ' = ', a.balance_recharge_micro, ' + ', a.balance_bonus_micro) AS CHAR) AS detail,
       IF(a.balance_micro = a.balance_recharge_micro + a.balance_bonus_micro, 'PASS', 'FAIL') AS verdict
  FROM t_credit_account a WHERE a.user_id = @uid
UNION ALL
SELECT 'A2',
       '每批 remaining = face - consumed',
       CAST(CONCAT('不符批次 ', SUM(b.remaining_micro <> b.face_value_micro - b.consumed_micro),
                   ' / 共 ', COUNT(*), ' 批') AS CHAR),
       IF(SUM(b.remaining_micro <> b.face_value_micro - b.consumed_micro) = 0, 'PASS', 'FAIL')
  FROM t_credit_batch b WHERE b.user_id = @uid
UNION ALL
SELECT 'A3',
       'SUM(流水) = balance（且无孤儿流水）',
       CAST(CONCAT(l.total, ' = ', a.balance_micro, '，孤儿 ', l.orphans) AS CHAR),
       IF(l.total = a.balance_micro AND l.orphans = 0, 'PASS', 'FAIL')
  FROM (SELECT COALESCE(SUM(x.amount_total_micro), 0) AS total,
               SUM(x.batch_id IS NOT NULL
                   AND NOT EXISTS (SELECT 1 FROM t_credit_batch b2 WHERE b2.id = x.batch_id)) AS orphans
          FROM t_ledger_entry x WHERE x.user_id = @uid) l
  JOIN t_credit_account a ON a.user_id = @uid
UNION ALL
SELECT 'A4',
       '账号累计消耗 = SUM(用量 consumed)',
       CAST(CONCAT(a.total_consumed_micro, ' = ', COALESCE(u.total, 0)) AS CHAR),
       IF(a.total_consumed_micro = COALESCE(u.total, 0), 'PASS', 'FAIL')
  FROM t_credit_account a
  LEFT JOIN (SELECT SUM(x.consumed_micro) AS total FROM t_usage_record x WHERE x.user_id = @uid) u ON 1 = 1
 WHERE a.user_id = @uid
UNION ALL
SELECT 'A5',
       '流水借方合计 = 批次消耗合计',
       CAST(CONCAT(d.debits, ' = ', COALESCE(b.consumed, 0)) AS CHAR),
       IF(d.debits = COALESCE(b.consumed, 0), 'PASS', 'FAIL')
  FROM (SELECT COALESCE(-SUM(x.amount_total_micro), 0) AS debits
          FROM t_ledger_entry x WHERE x.user_id = @uid AND x.direction = 'DEBIT') d
  LEFT JOIN (SELECT SUM(y.consumed_micro) AS consumed FROM t_credit_batch y WHERE y.user_id = @uid) b ON 1 = 1;
