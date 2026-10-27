# T16 · 工单·审核中心·模型主数据 API

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T16 |
| 阶段 | 4 · 控制面 |
| 优先级 | P1 |
| 依赖 | T15（用户/模型/资金基础接口）、**T10**（错误码） |
| 可并行 | 与 T12 / T13 / T14 并行 |
| 交付根 | `internal/control/ticket/`、`internal/control/review/`、`internal/control/modelmaster/` |
| 状态 | 🟨 第一段已交付（2026-10-27）· 工单中心完成；审核中心与模型主数据未派 |

## 1. 目标

实现 2026-10-25 新增的三块能力：工单中心（用户 + 运营双端）、审核中心（五类单据统一审批）、模型主数据（定价表的唯一名称来源）。三者的共同点是**状态机 + SLA + 职责分离**，不是普通 CRUD。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| PRD §5.9 | 706–750 | 工单中心：创建、状态筛选、关联 `request_id` 选择器（90 天内本人）、内容申诉自动带出规则 ID |
| PRD §6.6 | 875–903 | 审核中心：五类单据、`PENDING` 默认 Tab、按提交时间**升序**、驳回必填理由、**不得自审** |
| PRD §6.7 | 903–938 | 模型主数据：改名同步定价表、别名冲突、上架状态变更生成草稿、推荐权重约束、筛选 |
| PRD §10.3 | 1229–1263 | 账号注销：存在未结工单时拒绝并列出工单号 |
| PRD D.3 | 1732–1748 | F19-1–F19-4、F20-1–F20-4、F21-1–F21-3、F22-1–F22-6 验收路径 |
| TDS §1.9 | 2884–2955 | `t_ticket` / `t_ticket_reply` / `t_review_task` 三表与状态机、SLA 字段 |
| TDS §8.4 | 4644–4835 | `content_policy_violation` 与命中记录（申诉关联来源） |

## 3. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `internal/control/ticket/create.go` | `POST /api/console/tickets`：类型（技术/账单/内容申诉/其他）、关联 `request_id`（可空）、正文 |
| 2 | `internal/control/ticket/create.go` | 内容申诉自动带出：命中规则 ID + `request_id`（从 `t_content_policy_hit` 读） |
| 3 | `internal/control/ticket/picker.go` | `request_id` 选择器：只列 **90 天内本人**请求；提交他人 `request_id` → 拒绝 |
| 4 | `internal/control/ticket/list.go` | 用户侧列表：状态筛选（`OPEN`/`RESPONDED`/`CLOSED`）+ 详情显示「已响应 / 剩余 xx」 |
| 5 | `internal/control/ticket/reply.go` | 运营回复：状态自动置 `RESPONDED` + 发邮件；内部备注（用户不可见） |
| 6 | `internal/control/ticket/reply.go` | 改状态 + 写内部备注，三条操作各自入审计（操作人/时间/前后状态/是否含内部备注） |
| 7 | `internal/control/ticket/sla.go` | SLA：`first_response_due_at` = 创建 + 4h；超时**不自动关闭**，只进告警区并可在列表筛选 |
| 8 | `internal/control/ticket/close.go` | 关闭：仅运营或用户主动；关闭前不可注销账号（PRD F19-4） |
| 9 | `internal/control/review/list.go` | `GET /api/admin/reviews`：五类单据统一列表；默认 `PENDING`；按**提交时间升序**（先到先审）；类型/状态筛选 |
| 10 | `internal/control/review/approve.go` | 通过：按类型分发副作用（提额 → 改策略；退款 → 走 T13；开票 → `ISSUED`；分组开通 → 改 `group_code`；渠道专属 → 改名单） |
| 11 | `internal/control/review/reject.go` | 驳回：**理由必填**（空 → 400）；用户侧对应入口可见理由 |
| 12 | `internal/control/review/sod.go` | 职责分离：`submitter_id == reviewer_id` → **系统级拒绝**（403 + 明确码），不是前端禁用按钮 |
| 13 | `internal/control/modelmaster/crud.go` | 模型主数据 CRUD：对外名（定价表唯一名称来源）、别名、类型、上下文、来源地、协议 |
| 14 | `internal/control/modelmaster/alias.go` | 别名唯一：别名 `x` 已属 M1，给 M2 配 `x` → 拒绝并列出冲突模型 |
| 15 | `internal/control/modelmaster/listing.go` | 上架状态变更：生成待发布公告草稿，记操作人与理由；`推荐权重` 联动失效（同 T15 规则） |
| 16 | `internal/control/modelmaster/sync.go` | 改名同步：`t_model.name` 变更后，定价表 / 广场 / 文档示例均取同一来源（**不落第二份文案**） |
| 17 | `internal/control/*/**_test.go` | F19/F20/F21/F22 全覆盖 |
| 18 | `api/openapi.yaml`（追加） | 本卡接口 schema |

## 4. 硬约束（违反即驳回）

1. **工单状态机只有三态**：`OPEN` / `RESPONDED` / `CLOSED`。**禁止** `AUTO_CLOSED`：PRD F21-2 明确要求超时不自动关闭（自动关闭会让 SLA 变成"准时关闭"而非"准时响应"）。
2. **SLA 只提示不处置**：4h 未响应 → 仍在 `OPEN` + 进告警区；**不得**自动回复、自动升级、自动关闭。
3. **审核默认排序 = 提交时间升序**（先到先审）。默认 Tab = `PENDING`。**不得**按更新时间或优先级排序。
4. **自审必须系统级拒绝**：`submitter_id == reviewer_id` → 403。不能靠"流程约定"或前端禁用。
5. **驳回理由必填**：后端校验非空（不仅是前端 required）。
6. **越权零泄露**：用户只能看自己的工单；`request_id` 越权提交一律拒绝，且**不区分**"不存在"与"不属于你"。
7. **内容申诉自动关联**：从 `t_content_policy_hit` 取规则 ID 与 `request_id`，**不得**让用户手填规则 ID（手填必然填错）。
8. **模型名唯一真相源**：`t_model.name` 是唯一来源；定价表、广场、文档示例不得各存一份名称。改名后所有展示同步（PRD F22-1）。
9. **别名全局唯一**（含大小写敏感：`qwen-Max ≠ qwen-max`）；冲突拒绝并列出冲突模型。
10. **审核副作用幂等**：同一单据重复审批只能生效一次；通过后单据状态不可回退到 `PENDING`。
11. **审批与资金耦合处必须走 T13/T04 的既有接口**，不得在本卡直接改余额。
12. **所有审批动作审计**：审批人、时间、单据、前后状态、理由。
13. **注销前校验**：存在 `OPEN`/`RESPONDED` 工单 → 拒绝注销并列出工单号（F19-4）。
14. **不得提供批量审批接口**（V1.0）——批量审批会绕过逐单理由校验。

## 5. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `go test ./internal/control/ticket/... ./internal/control/review/... ./internal/control/modelmaster/... -race` 全绿 | 复跑 |
| A2 | F19-1：选择器只列 90 天内本人请求；提交他人 `request_id` → 拒绝 | 单测 |
| A3 | F19-2：`OPEN`/`RESPONDED` 筛选正确；详情显示「已响应 / 剩余 xx」 | 单测 |
| A4 | F19-3：403 `content_policy_violation` 后提交申诉 → 自动带出规则 ID + `request_id` | 单测 |
| A5 | F19-4：存在 `RESPONDED` 工单 → 注销被拒并列出工单号 | 单测 |
| A6 | F20-1：默认 `PENDING` Tab，列表按提交时间**升序** | 单测 |
| A7 | F20-2：驳回不填理由 → 400；填理由后用户侧可见 | 单测 |
| A8 | F20-3：同一账号提交 + 审批 → **系统级拒绝** | 单测 |
| A9 | F20-4：通过提额 → 站内通知；发票 `ISSUED`、退款 `REFUNDED`/`REFUND_FAILED` 发邮件 | 单测 |
| A10 | F21-1：运营回复 → 状态自动 `RESPONDED` + 邮件 | 单测 |
| A11 | F21-2：超 4h 未响应 → 仍 `OPEN`、进告警区、**未被自动关闭** | 单测（假时钟） |
| A12 | F21-3：回复 + 改状态 + 内部备注 → 审计 3 条，字段齐备 | 单测 |
| A13 | F22-1：改名 → 定价表同步新名 | 单测 |
| A14 | F22-2：别名冲突 → 拒绝并列出冲突模型 | 单测 |
| A15 | F22-3 / F22-5 / F22-6：状态变更生成草稿；`CATALOG_ONLY` 设权重被拒；`LISTED` 权重生效且审计含前后值 | 单测 |
| A16 | F22-4：按来源地/类型/上架状态筛选；`CATALOG_ONLY` 行可单独标色（返回 `listing_state` 字段） | 单测 |
| A17 | 审批幂等：同一单据通过两次 → 副作用只发生一次 | 单测 |

## 6. 自检命令

```bash
go test ./internal/control/ticket/... ./internal/control/review/... ./internal/control/modelmaster/... -race -cover
grep -rn "AUTO_CLOSED" internal/ || echo "OK: 无自动关闭状态"
```

## 7. 禁止事项

- 不得给工单加第四种状态。
- 不得实现超时自动关闭/自动升级。
- 不得允许自审（哪怕加"确认"弹窗）。
- 不得批量审批。
- 不得让模型名称存在第二份来源。

## 8. 完成回执

```
卡号: T16
接口清单: 路径 / 方法 / 权限点 / 对应 F 编号
F 覆盖: F19-1..4、F20-1..4、F21-1..3、F22-1..6 → 用例名
SoD: A8 输出（拒绝码 + 证据）
SLA: A11 输出（证明未自动关闭）
工单状态机: 枚举值 + 与 PRD 一致性断言
审批副作用: 五类单据各自的副作用落点（文件:函数）
遗留:
```

---

## 9. 第一段交付记录（2026-10-27）· 工单中心

新增 `internal/control/ticket/`（`service/create/picker/list/reply/sla/close` + 7 个测试文件），改 `mw/authz.go`（`ticket.read`/`ticket.write` 仅授 `PERSONAL` 与 `SUPER_ADMIN`，OPS 只用 `ticket.reply`）、`server.go`、`openapi.yaml`。**未新增迁移、未新增配置键、未新增错误码**（复用 `resource_not_found` / `resource_state_conflict`）。

| 验收 | 结果 |
| :--- | :--- |
| 三态、无第四态 | ✅ `TestTicketStateEnumHasExactlyThreeValues`；`grep AUTO_CLOSED` 在 `*.go` 零命中（只剩 0015 迁移里那句禁止性注释） |
| A11 SLA 只提示不处置 | ✅ 假时钟 +5h：`state=OPEN`、`overdue=true`、告警区 1 条、回复 0、邮件 0；+53h 仍未关闭 |
| A2/F19-1 选择器与越权 | ✅ 只列 90 天内本人；他人 / 不存在 / 100 天前 三种情况**同一响应** 404 `resource_not_found` |
| A4/F19-3 内容申诉带出 | ✅ 从 `t_content_policy_hit` 取规则 ID 与 `request_id`；命中记录优先于用户传值 |
| A5/F19-4 注销前校验 | ✅ `CheckAccountCancellable` 列出未结工单号（`CLOSED` 不计入）；仓库无注销接口，未新建 |
| A10/F21-1 回复置 RESPONDED | ✅ 首次回复才写 `first_responded_at`（SLA 停表）+ 邮件（`Deps.Mailer`，未注入只 WARN） |
| A12 三条审计 | ✅ `REPLY` / `INTERNAL_NOTE` / `STATE_CHANGE` 各自一条，含前后状态与 `has_internal_note`，与业务同事务 |
| 内部备注隔离 | ✅ 过滤在 SQL（`is_internal = 0`），用户侧详情看不到；内部备注不置状态、不发邮件、不停 SLA |
| 越权他人工单 | ✅ 详情/关闭均 404，与不存在不可区分 |
| 全仓回归 | ✅ `./internal/...` **36 包 ok / 0 FAIL**；lint 8 条通过；`ticket` 覆盖率 74.3% |

### 遗留

1. **用户侧补充回复与重开未实现**：接口表未列用户回复路由，`CLOSED → OPEN` 不可达，`reopen_count` 只读。
2. **48h 答复倒计时未落**：`t_ticket` 无对应列，且没有用户回复路由，计时基准不存在；本段只落 4h 首次响应。
3. **运营侧无工单详情路由**：卡面接口表未列 `GET /api/admin/tickets/{id}`，运营目前只能看列表，内部备注只能写不能读回（用户侧已确认不可见）。这是接口表本身的缺口，留待补卡。
4. 内容申诉额外按 TDS §8.4.5 置 `action='APPEAL_PENDING'`、写 `appeal_at`/`appeal_note`，并拒 30 天外/已复核的命中——卡面只写「自动带出」，此为源文档补充口径。
