# T13 · 资金域 API（订单/退款/发票/提额/套餐）

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T13 |
| 阶段 | 4 · 控制面 |
| 优先级 | P0（资金） |
| 依赖 | T11、T04、**T10**（错误码与写出口） |
| 可并行 | 与 T12 / T14 / T15 并行 |
| 交付根 | `internal/control/fund/` |
| 状态 | ⬜ 未开始 |

## 1. 目标

实现「用户怎么付钱、怎么退钱、怎么要票、怎么提额」四件事的后端，以及套餐购买与积分分账规则。核心不是页面，而是 **G-03（支付回调幂等）、G-04（退款）、G-07（开票）、G-08（提额）、G-09（体验版防刷）** 五条 GWT 的可执行性。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| PRD §5.6 | 615–643 | 充值中心：套餐、自定义金额、兑换码、订单列表、退款、开票 |
| PRD §3.1 | 199–242 | 积分分账（充值/赠送独立记账）、扣减顺序、可退基数 |
| PRD §3.2.1 | 281–326 | 套餐一次性购买、取高不叠加、跨档升档、赠送积分 90 天 |
| PRD 附录 D G-03/G-04/G-07/G-08/G-09 | 1386–1516 | 五条 GWT，**逐条成为用例** |
| PRD §5.1 F5-1 | 519 | 提额申请入口 |
| PRD §9.11 | 1141–1179 | 对账差异与资金处置（退款/冲正边界） |
| TDS §1.5 | 2023–2419 | `t_recharge_order` / `t_refund_order` / `t_invoice_apply` / `t_quota_apply` / 批次 |
| TDS §5.5 | 3887–3929 | 退款冻结与解冻、可退基数四条 |

## 3. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `internal/control/fund/plan.go` | `GET /api/console/plans`：四档 + 默认档展示数据（全部来自 `t_plan` 与 `t_rate_limit_policy`，**禁止硬编码**） |
| 2 | `internal/control/fund/order.go` | `POST /api/console/orders` 创建充值/套餐订单：金额、档位、幂等键（客户端可传 `Idempotency-Key`） |
| 3 | `internal/control/fund/order.go` | `GET /api/console/orders`：状态筛选（待支付/已支付/已关闭/已退款）+ 订单号搜索（交集） |
| 4 | `internal/control/fund/callback.go` | 支付回调：验签 → **幂等**（同 `out_trade_no` 只入账一次） → 分账入账（充值积分 + 赠送积分两批次）→ 触发 K26 失效 |
| 5 | `internal/control/fund/callback.go` | 回调重放：重复通知返回成功但**不重复加积分**；失败通知保留可重试记录 |
| 6 | `internal/control/fund/plan_grant.go` | 套餐权益到账：限流规格**取高不叠加**、跨档升档**覆盖**、新档权益有效期自支付成功起算、赠送积分不因升档延长 |
| 7 | `internal/control/fund/redeem.go` | 兑换码：单次有效、批次归属、兑换即入账（走 `t_ledger_entry`） |
| 8 | `internal/control/fund/refund.go` | `POST /api/console/refunds`：可退基数 = 未消耗**充值**积分（按批次 FIFO 反算）；赠送积分不可退；退款期间冻结对应余额 |
| 9 | `internal/control/fund/refund.go` | 退款状态机：`PENDING` → `APPROVED` → `REFUNDING` → `REFUNDED` / `REFUND_FAILED`；失败回滚冻结 |
| 10 | `internal/control/fund/invoice.go` | 开票申请：需实名（未实名 → 403 `account_not_verified`）；金额上限 = 已消费充值积分对应金额；`ISSUED` 后不可重复申请 |
| 11 | `internal/control/fund/quota_apply.go` | 提额申请：提交 → 审核中心（T16）→ 通过后改 `t_rate_limit_policy` 并写穿配置 |
| 12 | `internal/control/fund/trial.go` | 体验版防刷：设备指纹 + IP + 手机号/邮箱多因子；同一设备不得重复领取（G-09） |
| 13 | `internal/control/fund/notify.go` | 站内通知：发票 `ISSUED`、退款 `REFUNDED`/`REFUND_FAILED` 额外发邮件（PRD F20-4） |
| 14 | `internal/control/fund/*_test.go` | G-03/G-04/G-07/G-08/G-09 五组用例 + 回调重放 + 分账 + 升档 |
| 15 | `api/openapi.yaml`（追加） | 本卡接口 schema |
| 16 | `docs/fund-flow.md` | 资金状态机（mermaid）：订单 / 退款 / 发票 / 提额 四张 |

## 4. 硬约束（违反即驳回）

1. **回调幂等是硬要求**：同一 `out_trade_no` 的通知重复到达 N 次，积分只加一次、批次只建一次、流水只有一条。实现用唯一键（`t_recharge_order.out_trade_no`）+ 状态机 CAS，**不得只靠"先查后写"**。
2. **支付成功前积分不增加**：`t_recharge_order.status != PAID` 时任何入账路径都不可执行。
3. **分账不可合并**：充值积分与赠送积分**两个批次、两个 `expire_at`**（赠送从支付成功起 90 天；充值无到期）。合并成一个批次即驳回。
4. **退款只退未消耗充值积分**：赠送积分（含已过期清零部分）与已消费部分不可退。可退基数必须能由 `t_ledger_entry` 的批次级扣减明细复算（T04 已保证每批次一条 DEBIT）。
5. **退款金额精度**：全微积分整数；展示层折算元（÷100）。
6. **退款期间冻结**：`REFUNDING` 状态下冻结对应批次余额，防"退款中又消费"造成超退。
7. **套餐不叠加**：四项限流规格取**最高值**，不做求和；跨档购买视为升档覆盖。重复购同档 = 积分累加 + 规格不变。
8. **默认档不可购买**：`FREE_DEFAULT` 不出现在可购买列表。
9. **金额类接口必须幂等**：创建订单、申请退款、申请提额都接受 `Idempotency-Key`，10s 窗口去重（与网关同语义）。
10. **提额审批不得自审**：审批由 T16 审核中心做，本卡只做提交与生效；同一账号操作人与审批人相同 → 系统拒绝。
11. **开票需实名**，且**税率口径固定 `pricing.tax_rate_multiplier=1.00`**，函数首行 `TODO(Finance)`；**不得**自行填税率或价税分离逻辑。
12. **体验版防刷**：同一设备指纹 + IP 段 + 手机号任一命中已领取记录即拒；**不得**仅靠邮箱（可无限注册）。
13. **所有资金写操作必须审计**（T11 审计中间件）+ 产生 `t_ledger_entry`（充值/退款/兑换/清零各为独立类型）。
14. **通知失败不影响主流程**：通知/邮件失败只记 WARN。
15. **不得实现提现**（PRD C9 分销代理属 V2.0，Q-02 默认不做），也不得预留半成品接口。

## 5. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `go test ./internal/control/fund/... -race` 全绿 | 复跑 |
| A2 | **G-03**：同一回调并发 10 次 → 积分只加一次、批次 1 个、流水 1 条、返回均成功 | 单测 |
| A3 | **G-03**：`status=PENDING` 时调用入账 → 拒绝（积分不变） | 单测 |
| A4 | **G-04**：充 99 元（9900 + 2000），消费 1000 → 可退 = 8900 积分（扣的是赠送先）→ 实际退款额按未消耗充值积分算 | 单测（精确数值） |
| A5 | **G-07**：未实名申请开票 → 403 `account_not_verified`；已开票订单再申请 → 拒绝 | 单测 |
| A6 | **G-08**：提额申请 → 审核通过 → `t_rate_limit_policy` 变更且 K26/配置版本生效 | 单测 + 集成 |
| A7 | **G-09**：同设备指纹第二次领取体验积分 → 拒绝 | 单测 |
| A8 | 升档：STANDARD 在有效期，购 PRO → 规格取 PRO、`STANDARD` 的赠送批次仍存在且到期日不变 | 单测 |
| A9 | 重复购同档：规格不变、积分累加、两个批次各自到期 | 单测 |
| A10 | 退款状态机：`REFUND_FAILED` 后冻结解除、余额不变 | 单测 |
| A11 | 订单筛选：3 种状态 + 订单号 → 返回交集 | 单测 |
| A12 | 金额精度：全链路无浮点；`grep -rn "float" internal/control/fund` 为空 | grep |
| A13 | 提额自审：同一账号提交 + 审批 → 拒绝 | 单测 |
| A14 | `docs/fund-flow.md` 四张状态机与代码状态枚举一致（测试断言集合相等） | 单测 |

## 6. 自检命令

```bash
go test ./internal/control/fund/... -race -cover
grep -rn "float32\|float64" internal/control/fund/ || echo "OK"
M() { docker exec -i $(docker compose -f deploy/docker-compose.yml ps -q mysql) mysql -uroot -proot yunshu -N -e "$1"; }
M "SELECT status,COUNT(*) FROM t_recharge_order GROUP BY status"
M "SELECT kind,SUM(amount_micro) FROM t_ledger_entry GROUP BY kind"
```

## 7. 禁止事项

- 不得把赠送积分与充值积分合并成一次入账。
- 不得在回调里做"先查状态再写"的两步幂等（必须唯一键/状态 CAS）。
- 不得让退款走"直接扣减余额"而不动批次（否则可退基数永久失真）。
- 不得自行设计税率或价税分离。
- 不得实现提现/分销结算。

## 8. 完成回执

```
卡号: T13
接口清单: 路径 / 方法 / 权限点 / 对应 G 或 F 编号
G-03 幂等: 实现方式（唯一键/CAS）+ A2 输出
G-04 可退: 计算过程 + A4 输出（精确数值）
G-07/G-08/G-09: 各一条输出
分账: 两批次证据（贴 SQL：batch kind/expire_at）
状态机: 四张图与枚举一致性断言结果
遗留:
```
