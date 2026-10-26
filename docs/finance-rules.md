# 账本与计费内核 · 条款-函数-用例对照

审计范围：T04 交付的 `internal/billing/`、`internal/ledger/`。金额单位一律微积分 `_micro`；支付金额 `_fen` 不进入本包。

| 条款 | 函数 / 实现位置 | 测试用例 |
| :--- | :--- | :--- |
| G-01 一次请求只结算一次；费用 `(1200×1500+800×4500+999)÷1000=5,400` | `billing.CostMicro`（`internal/billing/cost.go:23`）；`ledger.(*Store).Settle`（`internal/ledger/settle.go`） | `TestG01CostMatchesHandCalculation`、`TestG01SettleOnceAndIdempotentRetry` |
| G-01 失败尝试不计费（`tokens_in=tokens_out=0`） | T07/T08 写 `t_usage_record`/`t_upstream_attempt`；T04 只保证 `ConsumedMicro=0` 全额释放 | `TestA8FailedRequestReleasesFullPrehold` |
| G-02 准入判据是 `raw_est`，不是 `est` | `ledger.Admit`（`internal/ledger/prehold.go:57`） | `TestG02AdmitUsesRawEstimate`、`TestG02PreholdAndSettleBoundary` |
| G-02 预扣额 `MIN(est, available×50%)` 向下取整 | `ledger.HoldMicro`（`internal/ledger/prehold.go:63`） | `TestG02HoldIsFiftyPercentOfAvailable`、`TestG02HoldTruncatedButRequestAdmitted` |
| G-02 余额 6,500、`raw_est` 6,000、`est` 7,200 ⇒ 放行，`hold=3,250` | `Prehold` + `Admit` + `HoldMicro` | `TestG02PreholdAndSettleBoundary`（断言 `hold=3250`；卡片原文的 `3600` 是 `est×50%`，见下方差异） |
| G-02 小额负余额：余额 3,000、预扣 1,500、实际 3,500 ⇒ −500；充值 5,000 ⇒ 4,500 | `Settle` + `Recharge` | `TestG02SmallNegativeBalanceAndRecharge` |
| G-02 截断五条：上限 `available+overdraft`；超出写 `t_arrears`；置 `FROZEN_OVERDRAFT`；充值先冲欠费；透支不作准入 | `Settle`（`internal/ledger/settle.go:175` 起）、`insertArrearsTx`（`internal/ledger/arrears.go:29`）、`Recharge`（`internal/ledger/arrears.go:196`） | `TestA4TruncationExactlyAtCap`、`TestA4TruncationWithArrearsAndFreeze`、`TestG02HardTruncationAndRechargeRecovery` |
| G-04 可退基数只含未消耗充值批次；赠送不可退；已消耗不可退 | `QuoteRefund`/`quoteRefundTx`（`internal/ledger/refund.go`） | `TestG04RefundBaseExcludesBonusAndConsumed` |
| G-04 退款金额 = 7,900 ÷ 100 × 90% = 71.10 元 | `RefundFen`（`internal/ledger/refund.go:40`） | `TestG04RefundBaseExcludesBonusAndConsumed`（7,110 分） |
| G-04 起退额 1,000 积分（10 元）；已开票拒绝；PENDING 需先撤销 | `quoteRefundTx` | `TestG04MinimumRefundThreshold`、`TestG04IssuedInvoiceBlocksRefund`、`TestG04PendingInvoiceRequiresRevoke` |
| G-05 调价不回溯；新请求按新版本；历史行 `price_rule_id` 不变 | `SelectPrice`（`internal/billing/price.go`）；账本 append-only（无 `updated_at`/`deleted_at`） | `TestG05PriceChangeDoesNotRewriteHistory`、`TestLedgerTableHasNoMutableColumns` |
| G-05 涨价 7 天公示期 | T15 后台保存校验，不在 T04 | 未覆盖（T15） |
| G-06 地板 `std×ratio×7500 ≥ cost×10000×10000`；整数交叉相乘 | `CheckFloorUnit`（`internal/billing/floor.go:37`） | `TestA9FloorPass`、`TestA9FloorRejectWithShortfall`、`TestG06ContractRatioRejected`、`TestG06ExactBoundary` |
| G-06 `internal`（ratio=0）不参与地板 | `CheckFloorUnit` 的 `Skipped` 分支 | `TestInternalGroupSkipsFloor` |
| G-06 缺成本价返回 `ok=false`，不得按 0 | `CheckFloorUnit` 的 `MissingCost` 分支 | `TestMissingCostIsNotZero` |
| 唯一舍入点；禁 per-token 舍入 | `billing.CostMicro`（`internal/billing/cost.go:23`） | `TestPrecisionAtSubCreditUnit`：1000→1500、1001→1502 |
| 长上下文整单跳价 | `SelectTierPrices`/`RequestCost`（`internal/billing/cost.go`） | `TestLongContextIsWholeOrderJump`、`TestLongContextThresholdIsStrictlyGreater` |
| 缓存计费结构就绪、默认关闭 | `EffectiveInputTokens`（`internal/billing/cost.go`） | `TestCacheBillingDisabledIgnoresRatios`、`TestCacheBillingEnabledWeightsInput` |
| 取价链 MODEL > CHANNEL | `SelectPrice`（`internal/billing/price.go`） | `TestPriceChainModelWinsOverChannel`、`TestPriceChainFallsBackToChannel` |
| 预扣→结算→释放幂等键 | `prehold:{rid}`、`settle:{rid}`、`release:{rid}`（`internal/ledger`） | `TestPreholdIsIdempotentByRequestID`、`TestG01SettleOnceAndIdempotentRetry` |
| 失败释放必须存在预扣流水 | `settleTx` 的 `ErrPreholdLedgerMissing` 分支 | `TestA8ReleaseWithoutPreholdLedgerIsRejected` |
| 扣减顺序：赠送(`expire_at,created_at`) → 充值 FIFO；每批次一条 DEBIT | `deductBatchesTx`/`deductBatchGroup`（`internal/ledger/deduct.go`） | `TestA6BatchOrderAndPerBatchEntries`、`TestBonusSameExpiryUsesCreatedAt` |
| 余额 CAS 与流水同事务 | `LockAccount` + `AppendEntry` + `Account.Apply`（`internal/ledger/balance.go`） | `TestConcurrentDebitsDoNotOverdraw`、`TestDebitLedgerFailureRollsBackBatchAndAccount` |
| 套餐四维取高不叠加、跨档覆盖、两套有效期 | `EffectiveLimits`、`ApplyPlanPurchase`、`BonusExpireAt`、`EntitlementExpireAt`（`internal/billing/plan.go`） | `TestLimitsTakeMaxNotSum`、`TestCrossTierUpgradeCoversOldPlan`、`TestBonusExpiryIsSeparateFromEntitlement`、`TestTrialFallsBackToDefaultAfterExpiry`、`TestPaidPlanDoesNotFallBack` |

## 实现口径与源文档差异

| 项 | 文档写法 | 实现口径 | 依据 |
| :--- | :--- | :--- | :--- |
| G-02 预扣额 | 卡片 A3 写 `hold==3600（=6500×50%）` | `hold=3250`（`6500×50%`）；`3600` 是 `7200×50%`，会突破 `hold ≤ available×50%` | PRD §3.1「预扣额 ≤ 可用余额 × 50%」；`min(est, available×50%)` |
| 退款金额公式 | TDS §1.5 `refund_fen = apply_micro × 9 / 1000` | `refund_fen = micro × 9 / 100000`；7,900 积分 = 79,000,000 微积分 ⇒ 7,110 分 = 71.10 元 | G-04 的 71.10 元是验收值；TDS 公式比它大 100 倍 |
| G-04 退款后余额 | 「余额中剩余的 2,000 充值积分仍可用于消费」 | 退掉 7,900 积分后充值批次剩余=0，余额只剩赠送 1,000 积分 | G-04 的「可退基数 = 9,900 − 2,000 = 7,900」与批次状态 |
| 透支上限注入 | 卡片要求阈值参数注入 | 透支上限取 `t_credit_account.overdraft_limit_micro`（物化账本字段，测试用 fixture 注入）；不读 `t_config_item` | TDS §1.5 将透支上限物化在账户行 |
| 结算流水拆分 | TDS §5.1 写 `CONSUME_SETTLE + PREHOLD_RELEASE` 两条 | 结算写 `PREHOLD_RELEASE` 全额释放 + 每批次一条 `CONSUME_SETTLE`；超透支部分另写 `batch_id=NULL`、`reason='OVERDRAFT_PORTION'` | 卡片硬约束 9「每批次一条 DEBIT，SUM == 总扣费」；账本净额仍为 `-consumed` |

## 未纳入 T04 的条款

- G-01 的 `t_usage_record` / `t_upstream_attempt` 行、`X-Credits-Consumed` 响应头：T07/T08。
- G-03 支付回调幂等、套餐到账、赠送批次到期清零：T13。
- G-05 涨价 7 天公示期、G-06 缺成本价的「警告 + 二次确认」后台交互：T15。
- 结算死信 `t_settle_task` 的 worker 消费：T07/T09；T04 只写结算内核。
