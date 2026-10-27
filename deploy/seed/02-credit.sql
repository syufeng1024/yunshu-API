-- ============================================================================
--  云枢 API 中转站 · 演示数据播种 02：demo 账号的资金账本
-- ============================================================================
--  调用方：scripts/seed-demo.sh
--  前置变量：@uid（演示账号 t_user.id）
--
--  规模（对应 PRD G-04 的 99 元档）：
--    充值 99 元 = 9900 积分（1 元 = 100 积分 = 1,000,000 微积分）+ 赠送 2000 积分
--    再消耗 435,000 微积分（43.5 积分，构成见 03-usage.sql）
--    余额 = 11900 - 43.5 = 11856.5 积分
--
--  账本必须满足 internal/ledger/invariant_test.go 的四条不变量：
--    1) SUM(t_ledger_entry.amount_total_micro) == t_credit_account.balance_micro
--    2) balance_micro == balance_recharge_micro + balance_bonus_micro
--    3) 每批 remaining_micro == face_value_micro - consumed_micro
--    4) 每条流水都指向存在的账号 / 批次
--  因此本文件不直接写死余额，而是「先写流水，再从流水汇总出账号与批次的
--  消耗列」——余额、批次剩余、累计消耗三处永远由同一条流水推导，重跑不会叠加。
--
--  幂等策略：
--    * 订单按 uk_ro_order_no 固定号 upsert；批次按 uk_cb_no 固定号 upsert；
--    * 流水先按「演示数据哨兵」精确删除，再整批重插：
--        user_id = @uid 且 (request_id LIKE 'demo-seed-%'
--                           OR idem_key IN ('recharge:<订单id>', 'bonus:<订单id>'))。
--      哨兵不会命中真实业务行：真实充值的 idem_key 是 recharge:{订单id}，
--      而这里的订单 id 就是本脚本建立的演示订单。
--    * 幂等键沿用真实写入约定（internal/control/fund/callback.go）：
--      recharge:{order_id} / bonus:{order_id} / settle:{request_id}。
--
--  简化说明：真实结算链路会写 CONSUME_PREHOLD(-X) + PREHOLD_RELEASE(+X) +
--  CONSUME_SETTLE(-X) 三行；本脚本没有 t_prehold_hold 行，故只写终态
--  CONSUME_SETTLE 一行。净额、批次 consumed、余额三者与真实结算完全一致，
--  差别仅在于「预扣/释放」这两步中间态不可见。
-- ============================================================================

SET NAMES utf8mb4 COLLATE utf8mb4_0900_as_cs;
SET @now := UTC_TIMESTAMP(3);

-- 演示订单 / 批次的固定业务键（char(20) 批次号：yyyymmdd + 6 位序号）
SET @order_no       := '2026010100000000000000000001';
SET @batch_recharge := '20260101000001';
SET @batch_bonus    := '20260101000002';

SET @recharge_micro := 99000000;  -- 9900 积分
SET @bonus_micro    := 20000000;  -- 2000 积分

-- 时间轴：入账在 30 天前，消耗分布在最近 3 天。
-- 「今天」的锚点按 CST（UTC+8）自然日算，与 internal/control/user/overview.go
-- 的 startOfDayCST 同构：@cst_midnight 是今日 00:00 CST 对应的 UTC 时刻。
SET @cst_midnight := DATE(@now + INTERVAL 8 HOUR) - INTERVAL 8 HOUR;
SET @today_span   := TIMESTAMPDIFF(SECOND, @cst_midnight, @now);
SET @t_today_a    := @cst_midnight + INTERVAL (@today_span DIV 4) SECOND;
SET @t_today_b    := @cst_midnight + INTERVAL (@today_span DIV 2) SECOND;

SET @t_req08 := @now - INTERVAL 3 DAY - INTERVAL 4 HOUR;
SET @t_req06 := @now - INTERVAL 2 DAY - INTERVAL 1 HOUR;
SET @t_req04 := @now - INTERVAL 1 DAY - INTERVAL 6 HOUR;
SET @t_req05 := @now - INTERVAL 1 DAY - INTERVAL 2 HOUR;

-- ---------------------------------------------------------------------------
-- 1. 充值订单（99 元 / 9900 充值积分 + 2000 赠送积分）
--    消费全部来自赠送批次，故 refundable_micro 仍是全额 9900 积分。
-- ---------------------------------------------------------------------------
INSERT INTO t_recharge_order (
  order_no, user_id, order_type, plan_id, pay_amount_fen,
  recharge_micro, bonus_micro, pay_channel, pay_txn_id, state, paid_at,
  refundable_micro, invoiced_fen, expire_at, created_at, updated_at
) VALUES (
  @order_no, @uid, 'CUSTOM', NULL, 9900,
  @recharge_micro, @bonus_micro, 'WECHAT', 'DEMO-SEED-PAY-0001', 'PAID',
  @now - INTERVAL 30 DAY,
  @recharge_micro, 0,
  @now - INTERVAL 30 DAY + INTERVAL 30 MINUTE,
  @now - INTERVAL 30 DAY, @now - INTERVAL 30 DAY
) AS new
ON DUPLICATE KEY UPDATE
  user_id          = new.user_id,
  pay_amount_fen   = new.pay_amount_fen,
  recharge_micro   = new.recharge_micro,
  bonus_micro      = new.bonus_micro,
  state            = new.state,
  paid_at          = new.paid_at,
  refundable_micro = new.refundable_micro,
  invoiced_fen     = new.invoiced_fen,
  updated_at       = new.updated_at;

SET @order_id := (SELECT id FROM t_recharge_order WHERE order_no = @order_no LIMIT 1);

-- ---------------------------------------------------------------------------
-- 2. 两个积分批次
--    RECHARGE：可退、不过期（B-02 / B-12）
--    BONUS   ：不可退、90 天到期（expire_at = effective_at + 90 天）
--    consumed/remaining 先归零，第 4 步再从流水重新推导。
-- ---------------------------------------------------------------------------
INSERT INTO t_credit_batch (
  user_id, batch_no, batch_type, source_id, face_value_micro,
  remaining_micro, consumed_micro, refundable, effective_at, expire_at,
  cleared_at, created_at, updated_at
) VALUES
  (@uid, @batch_recharge, 'RECHARGE', @order_id, @recharge_micro,
   @recharge_micro, 0, 1, @now - INTERVAL 30 DAY, NULL,
   NULL, @now - INTERVAL 30 DAY, @now),
  (@uid, @batch_bonus, 'BONUS', @order_id, @bonus_micro,
   @bonus_micro, 0, 0, @now - INTERVAL 30 DAY, @now - INTERVAL 30 DAY + INTERVAL 90 DAY,
   NULL, @now - INTERVAL 30 DAY, @now)
AS new
ON DUPLICATE KEY UPDATE
  source_id        = new.source_id,
  face_value_micro = new.face_value_micro,
  remaining_micro  = new.remaining_micro,
  consumed_micro   = new.consumed_micro,
  refundable       = new.refundable,
  effective_at     = new.effective_at,
  expire_at        = new.expire_at,
  cleared_at       = NULL,
  updated_at       = new.updated_at;

-- ---------------------------------------------------------------------------
-- 3. 清掉上一轮播种的流水（只删演示哨兵行）
-- ---------------------------------------------------------------------------
DELETE FROM t_ledger_entry
 WHERE user_id = @uid
   AND (
        request_id LIKE 'demo-seed-%'
     OR idem_key LIKE 'settle:demo-seed-%'
     OR idem_key = CONCAT('recharge:', @order_id)
     OR idem_key = CONCAT('bonus:', @order_id)
   );

-- ---------------------------------------------------------------------------
-- 4. 重插 8 条流水：2 条入账 + 6 条消耗
--    balance_before / balance_after 由窗口函数按 seq 顺序现算，而不是写死，
--    所以金额改了下游列也自动一致（日对账第一断言靠它成立）。
--    消耗顺序与真实扣减策略一致：先扣 BONUS 批次（B-01 A 档），
--    因此 6 条 DEBIT 的 batch_id 都指向 BONUS 批次。
-- ---------------------------------------------------------------------------
INSERT INTO t_ledger_entry (
  user_id, entry_no, account_type, direction, biz_type,
  amount_bonus_micro, amount_recharge_micro, amount_total_micro,
  balance_before_micro, balance_after_micro,
  batch_id, request_id, recharge_order_id, idem_key, created_at
)
SELECT @uid,
       CONCAT('0SEED', LPAD(s.seq, 21, '0')),   -- 26 字符，ULID 形状（Crockford 字母表内）
       s.account_type, s.direction, s.biz_type,
       s.amt_bonus, s.amt_recharge, s.amt_total,
       s.bal_before, s.bal_after,
       b.id,
       s.request_id,
       CASE WHEN s.uses_order = 1 THEN @order_id ELSE NULL END,
       s.idem_key,
       s.created_at
  FROM (
        SELECT r.seq, r.account_type, r.direction, r.biz_type,
               r.amt_bonus, r.amt_recharge, r.amt_total,
               r.batch_no, r.request_id, r.uses_order, r.idem_key, r.created_at,
               COALESCE(SUM(r.amt_total) OVER (
                          ORDER BY r.seq ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0) AS bal_before,
               SUM(r.amt_total) OVER (
                          ORDER BY r.seq ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS bal_after
          FROM (
                -- 入账：充值 9900 积分
                SELECT 1 AS seq, 'RECHARGE' AS account_type, 'CREDIT' AS direction, 'RECHARGE' AS biz_type,
                       0 AS amt_bonus, @recharge_micro AS amt_recharge, @recharge_micro AS amt_total,
                       @batch_recharge AS batch_no, NULL AS request_id, 1 AS uses_order,
                       CONCAT('recharge:', @order_id) AS idem_key,
                       @now - INTERVAL 30 DAY AS created_at
                UNION ALL
                -- 入账：赠送 2000 积分
                SELECT 2, 'BONUS', 'CREDIT', 'RECHARGE',
                       @bonus_micro, 0, @bonus_micro,
                       @batch_bonus, NULL, 1, CONCAT('bonus:', @order_id),
                       @now - INTERVAL 30 DAY + INTERVAL 1 SECOND
                UNION ALL
                -- 消耗（金额与 03-usage.sql 的 consumed_micro 逐笔对齐）
                SELECT 3, 'BONUS', 'DEBIT', 'CONSUME_SETTLE', -30000, 0, -30000,
                       @batch_bonus, 'demo-seed-req-08', 0, 'settle:demo-seed-req-08', @t_req08
                UNION ALL
                SELECT 4, 'BONUS', 'DEBIT', 'CONSUME_SETTLE', -45000, 0, -45000,
                       @batch_bonus, 'demo-seed-req-06', 0, 'settle:demo-seed-req-06', @t_req06
                UNION ALL
                SELECT 5, 'BONUS', 'DEBIT', 'CONSUME_SETTLE', -24000, 0, -24000,
                       @batch_bonus, 'demo-seed-req-04', 0, 'settle:demo-seed-req-04', @t_req04
                UNION ALL
                SELECT 6, 'BONUS', 'DEBIT', 'CONSUME_SETTLE', -240000, 0, -240000,
                       @batch_bonus, 'demo-seed-req-05', 0, 'settle:demo-seed-req-05', @t_req05
                UNION ALL
                SELECT 7, 'BONUS', 'DEBIT', 'CONSUME_SETTLE', -42000, 0, -42000,
                       @batch_bonus, 'demo-seed-req-01', 0, 'settle:demo-seed-req-01', @t_today_a
                UNION ALL
                SELECT 8, 'BONUS', 'DEBIT', 'CONSUME_SETTLE', -54000, 0, -54000,
                       @batch_bonus, 'demo-seed-req-02', 0, 'settle:demo-seed-req-02', @t_today_b
               ) r
       ) s
  LEFT JOIN t_credit_batch b
         ON b.user_id = @uid AND b.batch_no = s.batch_no
 ORDER BY s.seq;

-- ---------------------------------------------------------------------------
-- 5. 批次消耗从流水重新推导：consumed = 该批次上的扣减总额，remaining = face - consumed
--    （不变量 3）。create_ledger_id 回填入账流水，与 ledger.setBatchLedger 一致。
-- ---------------------------------------------------------------------------
UPDATE t_credit_batch b
   SET b.consumed_micro = COALESCE((
         SELECT -SUM(l.amount_total_micro)
           FROM t_ledger_entry l
          WHERE l.batch_id = b.id AND l.amount_total_micro < 0), 0),
       b.updated_at = @now
 WHERE b.user_id = @uid
   AND b.batch_no IN (@batch_recharge, @batch_bonus);

UPDATE t_credit_batch b
   SET b.remaining_micro = b.face_value_micro - b.consumed_micro
 WHERE b.user_id = @uid
   AND b.batch_no IN (@batch_recharge, @batch_bonus);

UPDATE t_credit_batch b
  JOIN t_ledger_entry l
    ON l.user_id = @uid AND l.idem_key = CONCAT('recharge:', @order_id)
   SET b.create_ledger_id = l.id
 WHERE b.user_id = @uid AND b.batch_no = @batch_recharge;

UPDATE t_credit_batch b
  JOIN t_ledger_entry l
    ON l.user_id = @uid AND l.idem_key = CONCAT('bonus:', @order_id)
   SET b.create_ledger_id = l.id
 WHERE b.user_id = @uid AND b.batch_no = @batch_bonus;

-- ---------------------------------------------------------------------------
-- 6. 账号余额同样从流水汇总（不变量 1 & 2）：
--      balance_recharge = SUM(amount_recharge_micro)
--      balance_bonus    = SUM(amount_bonus_micro)
--      balance          = 两列之和（= SUM(amount_total_micro)）
--      total_consumed   = CONSUME_SETTLE 借方的绝对值合计
--      version          = 账号被改写的次数；入账 2 次 + 结算 6 次
--      last_ledger_id   = 最后一条流水（日对账断点续算）
-- ---------------------------------------------------------------------------
SET @bal_recharge   := (SELECT COALESCE(SUM(amount_recharge_micro), 0) FROM t_ledger_entry WHERE user_id = @uid);
SET @bal_bonus      := (SELECT COALESCE(SUM(amount_bonus_micro), 0)    FROM t_ledger_entry WHERE user_id = @uid);
SET @total_recharge := (SELECT COALESCE(SUM(amount_recharge_micro), 0) FROM t_ledger_entry WHERE user_id = @uid AND amount_recharge_micro > 0);
SET @total_bonus    := (SELECT COALESCE(SUM(amount_bonus_micro), 0)    FROM t_ledger_entry WHERE user_id = @uid AND amount_bonus_micro > 0);
SET @total_consumed := (SELECT COALESCE(-SUM(amount_total_micro), 0)   FROM t_ledger_entry
                         WHERE user_id = @uid AND direction = 'DEBIT' AND biz_type = 'CONSUME_SETTLE');
SET @ledger_count   := (SELECT COUNT(*) FROM t_ledger_entry WHERE user_id = @uid);
SET @last_ledger_id := (SELECT MAX(id) FROM t_ledger_entry WHERE user_id = @uid);
SET @last_settled   := (SELECT MAX(created_at) FROM t_ledger_entry
                         WHERE user_id = @uid AND biz_type = 'CONSUME_SETTLE');

INSERT INTO t_credit_account (
  user_id, balance_recharge_micro, balance_bonus_micro, balance_micro,
  frozen_refund_micro, total_recharge_micro, total_bonus_micro, total_consumed_micro,
  overdraft_limit_micro, version, last_ledger_id, last_settled_at, created_at, updated_at
) VALUES (
  @uid, @bal_recharge, @bal_bonus, @bal_recharge + @bal_bonus,
  0, @total_recharge, @total_bonus, @total_consumed,
  COALESCE((SELECT u.overdraft_limit_micro FROM t_user u WHERE u.id = @uid), 500000),
  @ledger_count, @last_ledger_id, @last_settled, @now, @now
)
ON DUPLICATE KEY UPDATE
  balance_recharge_micro = @bal_recharge,
  balance_bonus_micro    = @bal_bonus,
  balance_micro          = @bal_recharge + @bal_bonus,
  frozen_refund_micro    = 0,
  total_recharge_micro   = @total_recharge,
  total_bonus_micro      = @total_bonus,
  total_consumed_micro   = @total_consumed,
  version                = @ledger_count,
  last_ledger_id         = @last_ledger_id,
  last_settled_at        = @last_settled,
  updated_at             = @now;
