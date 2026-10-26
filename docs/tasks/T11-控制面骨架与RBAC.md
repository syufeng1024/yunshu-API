# T11 · 控制面骨架·会话鉴权·RBAC

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T11 |
| 阶段 | 4 · 控制面 |
| 优先级 | P0 |
| 依赖 | T03、T05 |
| 可并行 | 与 T07 / T08 并行 |
| 交付根 | `cmd/controld/`、`internal/control/` |
| 状态 | ⬜ 未开始 |

## 1. 目标

建立控制面（`/console/*`、`/admin/*`、`/api/v1/auth/*`）的骨架：会话鉴权、角色与权限、审计、请求校验与错误映射、OpenAPI 契约。本卡不做业务功能，只保证**后续 5 张卡的每个接口都天然具备鉴权、审计、越权防护**。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| PRD §1.3 | 47–58 | 三种角色与明确禁止项；**正式版必须移除「用户/运营视角切换器」** |
| PRD §4.4 | 487–496 | 登录 / 注册 / 体验积分领取 |
| PRD §6.3 | 787–811 | 用户管理、角色枚举 `PERSONAL/OPS/FINANCE/SUPER_ADMIN`、禁用来源 |
| PRD §10 | 1179–1263 | 内容安全、个人信息与设备指纹、账号注销 |
| TDS 第一篇 §1 | 159–210 | `controld` 无状态、`/console/*` `/admin/*` 走主库读写 |
| TDS §1.3 | 1195–1424 | `t_user` 状态、`role`、软删哨兵 |
| TDS §1.8 | 2698–2884 | `t_admin_audit_log`（只追加）、`t_notification`、`t_announcement` |
| TDS §6.2 | 3997–4137 | `permission_denied` / `account_disabled` 等码的分工 |
| PRD D.3 | 1737–1739 | F20-3：**同一账号不得既是操作人又是审批人，须系统级拒绝** |

## 3. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `internal/control/server.go` | `controld` 路由装配：`/api/v1/auth/*`、`/api/console/*`、`/api/admin/*`；统一中间件链 |
| 2 | `internal/control/mw/session.go` | 会话：`sid`（HttpOnly / Secure / SameSite=Lax）、`K20 sess:admin:{sid}` 2h、滑动续期上限 |
| 3 | `internal/control/mw/authz.go` | 角色 → 权限点映射（`user.read` / `key.write` / `fund.approve` / `price.write` / `model.write` / `ticket.reply` …）；**接口级校验，不依赖前端菜单** |
| 4 | `internal/control/mw/audit.go` | 写操作必落 `t_admin_audit_log`：操作人、时间、对象、动作、前后值、来源 IP、UA；**只追加** |
| 5 | `internal/control/mw/ratelimit.go` | 登录 / 验证码 / 敏感接口按 IP + 账号双维度限流（防爆破） |
| 6 | `internal/control/mw/csrf.go` | 写接口校验 `Origin`/`Referer` + 双提交 Cookie（or SameSite=Lax + 自定义头，择一并说明） |
| 7 | `internal/control/mw/recover.go` | panic 恢复 → 500 `internal_error`，**不泄露堆栈**，落 ERROR 日志 |
| 8 | `internal/control/errs.go` | 控制面错误 → `apierr` 码映射（复用 T10，不新建码） |
| 9 | `internal/control/paging.go` | 统一分页/排序/筛选参数：`page`/`page_size`（上限 100）/`sort`/`order`；非法值 → 400 |
| 10 | `internal/control/audit_read.go` | 操作日志查询接口（运营可见，`SUPER_ADMIN` 可查全量） |
| 11 | `internal/control/auth/login.go` | 登录：bcrypt(cost=12) + `password_algo` 按行判定；失败计数与锁定；**登录成功/失败均审计** |
| 12 | `internal/control/auth/register.go` | 注册：邮箱/手机唯一（软删用哨兵）、协议版本勾选（`agreement_version`）、设备指纹（PRD §10.2） |
| 13 | `internal/control/auth/trial.go` | 体验积分**主动领取**（`trial_claimed_at`），非自动发放；7 天后回落默认档 |
| 14 | `api/openapi.yaml` | 控制面契约（本卡只出骨架 + 鉴权/审计/分页组件，业务路径由 T12–T16 追加） |
| 15 | `internal/control/mw/*_test.go` | 越权矩阵（10 个角色 × 接口）、审计必落、限流、CSRF |
| 16 | `docs/control-api.md` | 权限点总表 + 角色矩阵 + 错误码映射 |

## 4. 硬约束（违反即驳回）

1. **服务端逐接口鉴权**：每个接口必须显式声明所需权限点；**不得**只靠"前端不显示菜单"。测试必须覆盖：用 `PERSONAL` 账号请求每个 `/api/admin/*` 接口 → 403 `permission_denied`。
2. **401 与 403 分工**：未登录 → 401 `invalid_api_key` 之外的会话码（`session_expired`，若 E.1 无此码则用 401 + `invalid_api_key` 语义一致的会话码，**必须在回执说明选了哪个**）；登录但角色不够 → 403 `permission_denied`。**不得**复用 `account_disabled`。
3. **职责分离（SoD）**：`OPS` 可提交审核单、不可自审；`FINANCE` 可审批资金类；`SUPER_ADMIN` 全权但**同样不得审批自己提交的单据**（PRD F20-3 要求系统级拒绝，不是流程约定）。
4. **审计不可绕过**：所有写操作（POST/PUT/PATCH/DELETE）必须留痕，且记录**前后值**。缺前后值的审计视为未实现。
5. **软删哨兵**：删除用户/Key 时把参与唯一索引的列改写为 `#del#{id}#{unix_ms}`，不用 `NULL`。
6. **不得返回他人数据**：所有查询强制注入 `owner_id = session.uid`（除 `OPS`/`SUPER_ADMIN` 的显式跨用户查询接口，且该接口须审计）。
7. **密码与设备指纹**：密码只存 bcrypt/argon2id 哈希；`password_hash` 列长 120 兼容升级；设备指纹存哈希不存原值。
8. **会话存储**：`K20` 只存会话 ID 与 uid，不存权限（权限实时按角色算，避免改角色后旧会话仍生效）。
9. **移除角色切换器**：任何"用户视角/运营视角"开关不得进入正式接口；鉴权只看账号真实角色。
10. **CSRF 与 CORS**：写接口必须校验来源；CORS 白名单化，禁止 `*` + credentials。
11. **分页上限 100**：禁止无上限全量导出接口（导出走异步任务 + 对象存储，属 T14）。
12. **错误码复用**：控制面不得新增错误码（65 个之内），缺码时用最接近的语义码并在回执登记「建议新增码」。

## 5. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `go test ./internal/control/... -race` 全绿 | 复跑 |
| A2 | 越权矩阵：`PERSONAL` 账号请求全部 `/api/admin/*` → 403；`OPS` 请求 `fund.approve` → 403 | 单测 |
| A3 | 未登录请求任一控制面接口 → 401（不是 403、不是 200 空数据） | 单测 |
| A4 | 审计：每个写接口 1 次调用产生 1 条 `t_admin_audit_log`，含前后值 | 单测（表驱动覆盖全部写接口） |
| A5 | SoD：`OPS` 提交的提额单，同一账号调用审批 → **系统级拒绝**（403 + 明确码） | 单测 |
| A6 | 会话：登出后旧 `sid` 立即失效；2h 无操作过期 | 单测 |
| A7 | CSRF：跨站 `Origin` 的 POST → 403 | 单测 |
| A8 | 登录限流：连续失败 5 次后需等待（返回 429 或明确锁定提示） | 单测 |
| A9 | 软删：删除用户后用同邮箱再注册 → 成功；旧行 `email` 为哨兵格式 | 单测 |
| A10 | 越权读：A 用户访问 B 的 Key 详情 → 403 或「不存在」，不泄露存在性 | 单测 |
| A11 | panic 恢复：注入 panic → 500，响应体无堆栈、无 SQL | 单测 |
| A12 | `api/openapi.yaml` 可被 `npx @redocly/cli lint` 通过 | 复跑 |
| A13 | `docs/control-api.md` 权限点与代码中声明一致（测试断言标题集合相等） | 单测 |

## 6. 自检命令

```bash
go test ./internal/control/... -race -cover
npx --yes @redocly/cli lint api/openapi.yaml
grep -rn "role ==" internal/control/ --include=*.go | grep -v authz | head   # 期望极少，权限判断集中在 authz
```

## 7. 禁止事项

- 不得把权限判断散落在 handler 里（必须集中在 `authz` 中间件 + 显式声明）。
- 不得用前端隐藏菜单代替服务端校验。
- 不得给控制面新增错误码。
- 不得在审计日志里存密码、Key 明文、上游 Key。

## 8. 完成回执

```
卡号: T11
接口清单: 本卡实现的骨架接口 + 每接口权限点
越权矩阵: A2 用例表（角色 × 接口 × 期望码）
审计覆盖: 写接口列表 + 是否每个都有前后值
SoD: A5 输出
会话: 存储键 / TTL / 失效方式
401 vs 403: 实际选用的会话错误码（说明依据）
openapi: lint 输出
遗留:
```
