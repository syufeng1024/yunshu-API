# 源文档勘误（交付轮）

范围：本轮验收中确认的 PRD / TDS 事实错误。与 `docs/tasks/errata.md`（E-01–E-34）的关系：E-25 / E-26 / E-01 已登记过同类问题，本表给出行号、实测值与回写动作，不重复裁定理由。

| 编号 | 文档位置 | 原文 | 实际 | 裁决（以谁为准） | 是否需回写 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| ERR-01 | TDS §3.1「总超时预算」表（`docs/技术架构设计文档 V1.2.md:3287`）；对照 TA-1.2 第 8 项、PRD P-8（第 1631 行）、TDS §9.4（第 4239 行） | 「流式（首字节）｜90s｜上游首字节 60s + 转换与 flush 缓冲 30s」 | `gw.stream_first_byte_ms=30000` 为**单次尝试**；90s 是总硬上限；验算 `30 + 0.15 + 30 + 29.5 = 89.65s ≤ 90s`。`internal/config` 键表与 T08/T22 均按 30s | **以 TA-1.2 / PRD P-8 / TDS §9.4 为准**：单次尝试 30s，总预算 90s。§3.1 表是未同步旧口径 | 是（TDS §3.1 表；本轮已改） |
| ERR-02 | PRD 附录 D.3 F1-1（`docs/_API 中转站 · 需求文档 V1.2.md:1667`）；对照 PRD 附录 E.1（第 1823 行）、TDS §6.2、`internal/gw/p07_model.go:55` | 「请求向量模型返回 **404** `model_not_found`」 | PRD E.1 状态列为 **400**，`internal/apierr/codes.go` 的 spec 为 400，网关实测 400。全仓无 404 的 `model_not_found` | **以 PRD E.1 + TDS + 实现为准，取 400**。D.3 的 404 是孤立笔误 | 是（PRD D.3 F1-1 改为 400） |
| ERR-03 | PRD 附录 D G-03 Then 第 2 条（第 1393 行）；对照 TDS §1.5 `t_recharge_order.state`、迁移 `0003_fund_usage_ops.sql` | 「`t_recharge_order` 仅 1 行且状态 **`SUCCESS`**」 | DDL 枚举为 `PENDING / PAID / CLOSED / REFUNDING / REFUNDED / PARTIAL_REFUNDED`，无 `SUCCESS`；实现与 GWT 断言均为 `PAID` | **以 DDL 为准，支付成功 = `PAID`**（与 `docs/tasks/errata.md` E-25 同一裁定） | 是（PRD G-03 改为 `PAID`） |
| ERR-04 | PRD 附录 D G-07 第 1 条（第 1470 行）；对照 TDS §1.5 `t_invoice_apply.state`、迁移 `0003_fund_usage_ops.sql` | 「生成申请单状态 **`PENDING`**」 | DDL 初始状态为 **`APPLIED`**，审批后 `ISSUED`；`matrix-gwt.md` G-07 实测 `APPLIED` | **以 DDL 为准，初始状态 = `APPLIED`**。PRD 的 `PENDING` 与审核中心五类单据默认 Tab 的 `PENDING` 同名不同义 | 是（PRD G-07 改为 `APPLIED`） |
| ERR-05 | PRD 附录 E.1（第 1758 行计数口径、第 1764 行起 65 行）；TDS §6.2（第 4046–4059 行）vs `internal/apierr/codes.go` | PRD：「E.1 共 **65** 条……枚举表实际行数 == 本文声明数 == 65」；TDS：「摘要栏原称 48、本表实际 **54 行**、代码 registry 实际 **47 个**，以 47 为准」 | `internal/apierr/codes.go` 实际 **76** 个常量 / 76 条 spec；PRD 基线 65 + 实现新增 11（附录见 `matrix-codes.md`）。TDS 的 48/54/47 三个数均早于 T11–T16 的码表扩充，且 54 − 8 + 1 = 47 的算式已不成立 | **以代码 76 为唯一可枚举事实**；PRD E.1 保留 65 条基线并新增「实现期新增 11 码」附录，TDS §6.2 的 47 计数段整体作废。不得反向删码凑 65——`session_expired` / `key_limit_reached` 等码已有生产消费者 | 是（PRD E.1 计数口径 + TDS §6.2 计数段；本轮仅在 TDS §6.2 加勘误行） |
| ERR-06 | PRD 附录 D-2（第 1604 行）与 G-02（第 1378、1380 行）；对照 TDS §3.7 `billing.prestimate_safety_bp`、`internal/gw/p09_estimate.go:47-49` | 「预扣额用 `est = ⌈raw_est × 1.2⌉`」；G-02 断言 `raw_est=6,000 → est=7,200` | 网关 `est = raw × 1.1 × 1.2 = 7,920`；`billing.prestimate_safety_bp=11000` 在金额阶段乘入。TDS §3.7 已登记该键，PRD D-2 公式未含它 | **以实现公式为准**：`est = raw_est × preestimate_safety_bp × prehold_ratio`。理由：安全系数必须在唯一金额计算点生效，tokenizer 无法判断输入 token 是否已含余量。准入仍按 `raw_est`，G-02 的放行边界不变 | 是（PRD D-2 / G-02 补安全系数；本轮未改 PRD） |
| ERR-07 | TDS §6.2 码表（第 4044 行）；对照 PRD E.1（第 1953 行）、`internal/apierr/codes.go:726`、PRD §10.1（第 1208 行） | `content_policy_violation` ｜ `invalid_request_error` ｜ **400** ｜ 0 ｜ … | PRD E.1、PRD §10.1、F19-3 与实现均为 **403**；`apierr` spec 为 403。TDS 同文档第 4688 行也写 403 | **以 PRD E.1 + 实现为准，取 403**。400 与 403 的差别是「可改参数重试」与「内容终态」，混用会让客户端反复重试 | 是（TDS §6.2 第 4044 行；本轮已改） |
| ERR-08 | TDS §6.2 码表（第 4043 行）与 §3.3（第 2609 行）；对照 PRD E.1（第 1893 行）、`internal/apierr/codes.go:450` | `idempotency_key_reused` ｜ `invalid_request_error` ｜ **400** ｜ 0 ｜ …（第 2609 行同写 400） | PRD E.1 为 **409**（`conflict_error`）；`apierr` spec 为 409；`matrix-codes.md` 实测 409 | **以 PRD E.1 + 实现为准，取 409**。同键异体是冲突而非参数非法 | 是（TDS §6.2 第 4043 行、§3.3 第 2609 行；本轮已改） |

## 本轮 TDS 回写清单

| 位置 | 动作 | 对应 |
| :--- | :--- | :--- |
| §3.1 总超时预算表（3287 行） | 「上游首字节 60s + 转换与 flush 缓冲 30s」改为「单次尝试 30s + 转换与 flush 缓冲 29.5s（总预算 90s）」 | ERR-01 |
| §3.3 幂等同键异体（2609 行） | `idempotency_key_reused` 的 400 改为 409 | ERR-08 |
| §6.2 码表（4043、4044 行） | `idempotency_key_reused` 的 400/`invalid_request_error` 改为 409/`conflict_error`；`content_policy_violation` 的 400 改为 403 | ERR-07、ERR-08 |
| §6.2 错误码总数段（4046 行起） | 插入 2026-10-29 勘误行：48/54/47 三个数已作废，当前 76 码，65 基线 + 11 新增见 `matrix-codes.md` 附录 | ERR-05 |
| §8.6 迁移编号占用表 | 补 `0018`、`0019` 两行 | T23 §3 第 13 项 |

PRD 的 ERR-02–ERR-06 已登记，本轮**未改 PRD**（T23 §5.8 只允许回写 TDS 编号表与勘误；PRD 回写需交付总监单独执行）。
