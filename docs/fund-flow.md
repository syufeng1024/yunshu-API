# 资金状态机（fund flow）

真相源：`t_recharge_order` / `t_refund_order` / `t_invoice_apply` / `t_quota_apply` 的 DDL 枚举
（`internal/platform/migrate/sql/mysql/0003_fund_usage_ops.sql`）。**代码枚举以 DDL 为准**；
本文件与代码的一致性由 `internal/control/fund/flow_doc_test.go` 断言（集合相等，不是"看起来一样"）。

机器可读锚点（测试解析这两行，改动时三处必须同步改）：

```
fundspec:order-states PENDING,PAID,CLOSED,REFUNDING,REFUNDED,PARTIAL_REFUNDED
```

> 本段（T13 第一段）只实现订单状态机；退款 / 发票 / 提额三张图随第二段落地。
> 已发现的卡面与 DDL 冲突（退款状态机 `PENDING→APPROVED→REFUNDING→REFUNDED/REFUND_FAILED` 与
> `t_refund_order.state` 的 `APPLIED|APPROVED|REJECTED|PAYING|PAID|FAILED` 不一致）**以 DDL 为准**，
> 由交付侧登记勘误，第二段实现时按 DDL 枚举写。

## 1. 订单状态机（`t_recharge_order.state`）

```mermaid
stateDiagram-v2
    [*] --> PENDING: POST /api/console/orders（下单 +30 分钟到期）
    PENDING --> PAID: 支付回调验签通过 + 状态 CAS（PENDING→PAID）
    PENDING --> CLOSED: 过期未支付（关单任务）
    PAID --> REFUNDING: 退款申请受理（第二段）
    REFUNDING --> REFUNDED: 全额退款成功（第二段）
    REFUNDING --> PARTIAL_REFUNDED: 部分退款成功（第二段）
    REFUNDING --> PAID: 退款失败回滚冻结（第二段）
    REFUNDED --> [*]
    PARTIAL_REFUNDED --> [*]
    CLOSED --> [*]
```

### 每次迁移的副作用

| 迁移 | 触发 | 副作用（同事务） | 事务外 |
| :--- | :--- | :--- | :--- |
| `[*] → PENDING` | `POST /api/console/orders` | 写 `t_recharge_order`、`t_admin_audit_log(ORDER_CREATE)` | 无（**积分不动**） |
| `PENDING → PAID` | `POST /api/v1/pay/callback/{channel}` | `pay_txn_id` / `paid_at` / `refundable_micro` 落库、**充值批次 + 赠送批次**、**两条流水**、`t_rate_limit_policy` 生效（套餐单）、`t_user.plan_id`、审计 `ORDER_PAID` | `DEL K26`、`cfg:snap:ROUTING` 重发布 |
| `PENDING → CLOSED` | 关单任务（T14/T22） | 仅状态位 | 无 |
| `PAID → REFUNDING` | 第二段 | 冻结 `frozen_refund_micro` | 无 |

### 幂等的不变量（G-03）

1. `uk_ro_pay_txn(pay_channel, pay_txn_id)`：同一渠道流水号只能绑定一个订单；
2. `UPDATE ... WHERE id = ? AND state = 'PENDING'`：**状态 CAS**，同一订单只有一次入账机会；
3. `uk_le_idem`（`recharge:{order_id}` / `bonus:{order_id}`）：重复入账在账本层再被拒绝一次。

三层都在**同一个事务**里，没有"先查后写"的空窗。重放 N 次的结果：
余额只加一次、批次恰好 2 个、流水恰好 2 条（充值 1 + 赠送 1，PRD G-03 Then.3）。

## 2. 分账（每个 PAID 订单）

| 批次 | `batch_type` | `refundable` | `expire_at` | 流水 `biz_type` |
| :--- | :--- | :--- | :--- | :--- |
| 充值积分 | `RECHARGE` | 1 | **NULL（长期有效）** | `RECHARGE` / `PLAN_GRANT` |
| 赠送积分 | `BONUS` | 0 | **支付成功 + `billing.bonus_expire_days` 天** | 同上 |

兑换码入账走 `batch_type=REDEEM`（不可退），`biz_type=REDEEM`。

## 3. 套餐权益（`t_rate_limit_policy` + `t_user.plan_id`）

- 升档（`sort_order` 更大）：四项规格**整体覆盖**，`effective_to = NULL`（付费档永久）；
- 同档重复购：规格不变，积分按批次累加，赠送到期日各按自身；
- 低档订单（V1.0 不卖降档，仅兼容历史遗留）：四项**逐项取高**，`NULL` 日额度视为无穷大；
- 体验档：`effective_to = 支付时刻 + plan.trial_days`，到期回落默认档（余额不清空）。

## 4. 回调对外契约（`POST /api/v1/pay/callback/{channel}`）

| 响应 | 含义 | 渠道侧动作 |
| :--- | :--- | :--- |
| 200 `credited=true` | 首次入账成功 | 停止重投 |
| 200 `replayed=true` | 重复通知，已入账过 | 停止重投 |
| 200 `reason=ORDER_*` | 语义异常（订单不存在 / 已关闭 / 已退款 / 流水冲突），已写审计 | 停止重投，转人工 | 
| 400 `invalid_request_body` | 字段非法或**验签失败**（`message` 指明字段） | 告警，修正后重投 |
| 503 `config_unavailable` | 验签密钥未配置（`YUNSHU_PAY_CALLBACK_SECRET_{CHANNEL}`） | 退避重试 |

验签串：`out_trade_no|trade_no|amount_fen|status`，算法 `hex(HMAC-SHA256(secret, 串))`。
金额参与签名，防止"小额回调结大额订单"。
