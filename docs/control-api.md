# 控制面 API 约定

适用路径：`/api/v1/auth/*`、`/api/console/*`、`/api/admin/*`。路由表与权限声明在 `internal/control/server.go`，权限定义与角色映射在 `internal/control/mw/authz.go`。

## 权限点

| 权限点 | 含义 | 持有角色 |
| :--- | :--- | :--- |
| `user.read` | 读取自己的账号资料（控制台侧） | PERSONAL / OPS / SUPER_ADMIN |
| `user.write` | 启用、禁用用户（运营侧） | OPS / SUPER_ADMIN |
| `key.read` | 查看自己的 API Key 列表、详情与变更记录 | PERSONAL / SUPER_ADMIN |
| `key.write` | 创建、修改、轮换、删除自己的 API Key | PERSONAL / SUPER_ADMIN |
| `model.read` | 读取模型目录（控制台可用模型） | PERSONAL / OPS / FINANCE / SUPER_ADMIN |
| `model.write` | 创建、修改模型主数据、别名、上架状态与推荐权重 | OPS / SUPER_ADMIN |
| `price.write` | 创建、修改模型价格与价格分组 | OPS / SUPER_ADMIN |
| `group.write` | 修改价格分组倍率与可见范围 | OPS / SUPER_ADMIN |
| `channel.write` | 创建、修改、停用、删除上游渠道 | OPS / SUPER_ADMIN |
| `notice.write` | 创建、修改、发布公告 | OPS / SUPER_ADMIN |
| `risk.write` | 维护风控规则、查看命中、调整用户风险分 | OPS / SUPER_ADMIN |
| `config.write` | 读取与写入 `t_config_item` 运行时配置 | OPS / SUPER_ADMIN |
| `fund.read` | 查看自己的套餐、订单、退款、发票与提额单 | PERSONAL / FINANCE / SUPER_ADMIN |
| `fund.write` | 下单、兑换、申请退款/发票/提额、领取体验积分 | PERSONAL / SUPER_ADMIN |
| `fund.approve` | 审批退款、提额与资金类审核单 | FINANCE / SUPER_ADMIN |
| `ticket.read` | 读取自己提交的工单 | PERSONAL / SUPER_ADMIN |
| `ticket.write` | 创建工单、关闭自己的工单 | PERSONAL / SUPER_ADMIN |
| `ticket.reply` | 读取运营工单队列、回复、流转状态 | OPS / SUPER_ADMIN |
| `audit.read` | 查询 `t_admin_audit_log` | OPS / FINANCE / SUPER_ADMIN |
| `log.read` | 查询自己的请求日志与正文 | PERSONAL / SUPER_ADMIN |
| `usage.read` | 查询自己的用量、月账单、实时指标与 CSV 导出 | PERSONAL / SUPER_ADMIN |
| `admin.read` | 读取运营后台的用户、渠道、模型视图 | OPS / FINANCE / SUPER_ADMIN |
| `admin.all` | 跨域诊断（`GET /api/admin/whoami`） | SUPER_ADMIN |

`admin.read` 与 `model.read` / `user.read` 刻意分开：控制台读接口给客户看，运营读接口包含 `cost_*`、`base_url`、`key_mask`、`disable_reason`，共用权限点会把成本价和禁用原因泄露给普通账号（E-31 / E-33）。价格、分组、公告、风控、配置的运营读接口分别复用对应的 `.write` 权限点，FINANCE 只有 `admin.read`，因此能看渠道/模型/用户，不能看价格与风控。

## 角色 × 权限

| 权限点 | PERSONAL | OPS | FINANCE | SUPER_ADMIN |
| :--- | :---: | :---: | :---: | :---: |
| `user.read` | ✓ | ✓ | — | ✓ |
| `user.write` | — | ✓ | — | ✓ |
| `key.read` | ✓ | — | — | ✓ |
| `key.write` | ✓ | — | — | ✓ |
| `model.read` | ✓ | ✓ | ✓ | ✓ |
| `model.write` | — | ✓ | — | ✓ |
| `price.write` | — | ✓ | — | ✓ |
| `group.write` | — | ✓ | — | ✓ |
| `channel.write` | — | ✓ | — | ✓ |
| `notice.write` | — | ✓ | — | ✓ |
| `risk.write` | — | ✓ | — | ✓ |
| `config.write` | — | ✓ | — | ✓ |
| `fund.read` | ✓ | — | ✓ | ✓ |
| `fund.write` | ✓ | — | — | ✓ |
| `fund.approve` | — | — | ✓ | ✓ |
| `ticket.read` | ✓ | — | — | ✓ |
| `ticket.write` | ✓ | — | — | ✓ |
| `ticket.reply` | — | ✓ | — | ✓ |
| `audit.read` | — | ✓ | ✓ | ✓ |
| `log.read` | ✓ | — | — | ✓ |
| `usage.read` | ✓ | — | — | ✓ |
| `admin.read` | — | ✓ | ✓ | ✓ |
| `admin.all` | — | — | — | ✓ |

角色名以 `t_user.role` 为准，只有 `PERSONAL` / `OPS` / `FINANCE` / `SUPER_ADMIN` 四个（E-30）。未知角色在 `PermissionsFor` 中返回空集，登录后访问任何带权限的接口都是 403。

## 公开接口（官网）

5 条只读路径，无需 `sid` 会话，也无需 `X-CSRF-Token`；在 `internal/control/server.go` 中经 `s.public(...)` 注册（仅 panic 恢复，不进入 Session → CSRF → Require 链）。成功响应统一带 `Cache-Control: public, max-age=60`；错误响应（500 `internal_error`）不写该头，避免一次数据库故障被缓存 60 秒。

| 路径 | 数据源与过滤 | 排序 / 上限 | 响应 |
| :--- | :--- | :--- | :--- |
| `GET /api/public/plans` | `t_plan`，`enabled=1` | `sort_order ASC, id ASC` | `{data: PublicPlan[]}`；`daily_quota_micro` 为 NULL 时输出 `null`（FREE_DEFAULT 无上限），不转 0 |
| `GET /api/public/models` | `t_model`，`listing_state='LISTED'` 且 `deleted_at IS NULL`；`t_price_rule` 当前生效的 MODEL 级价格 | `recommend_weight DESC, published_at DESC` | `{data: PublicModel[]}`；无生效价或价格列为 NULL 的模型整行不返回；`context_window` 取自 `t_model.context_length` |
| `GET /api/public/announcements` | `t_announcement`，`state='PUBLISHED'`、`effective_from<=now`、`effective_to IS NULL OR > now`、`deleted_at IS NULL` | `effective_from DESC, id DESC`，最多 20 条 | `{data: PublicAnnouncement[]}`；`content→body`、`ann_type→category`、`effective_from→effective_at`、`created_at→published_at` |
| `GET /api/public/endpoints` | 无数据表（全部迁移中不存在 endpoint / integration / tool 表） | — | 恒为 `{data: []}`；`TODO(T24)` 在 `internal/control/publicapi/empty.go` |
| `GET /api/public/integration-links` | 同上 | — | 恒为 `{data: []}`；同上 |

公开模型的 `protocols` 只输出前端枚举内的 `OPENAI` / `ANTHROPIC`：`t_protocol.code='OPENAI_COMPAT'` → `OPENAI`，`ANTHROPIC_COMPAT` → `ANTHROPIC`，`BAILIAN` / `CUSTOM` 或 `protocol_id` 关联不到行时输出空数组。公告 `category` 只有 `PRICE` / `RATE_LIMIT` / `GENERAL` 三值：`ann_type='PRICE_CHANGE'` 映射为 `PRICE`，其余映射为 `GENERAL`（后端没有 `RATE_LIMIT` 枚举）。

## 错误码映射

只列控制面会返回的码。完整 76 码见 `docs/error-codes.md`。

| 错误码 | HTTP | type | 控制面场景 |
| :--- | :---: | :--- | :--- |
| `invalid_credentials` | 401 | `authentication_error` | 登录账号/密码错误；改密时旧密码错误 |
| `session_expired` | 401 | `authentication_error` | 无 `sid`、签名非法、K20 不存在、超过 8 小时绝对上限、改密后旧 sid |
| `account_disabled` | 403 | `authorization_error` | 登录或会话加载时 `t_user.status=DISABLED` |
| `account_frozen_due_to_overdraft` | 402 | `billing_error` | `t_user.status=FROZEN_OVERDRAFT` |
| `permission_denied` | 403 | `authorization_error` | 角色缺少路由声明的权限点；CSRF 双提交校验失败 |
| `account_exists` | 409 | `conflict_error` | 注册时邮箱或手机号已存在 |
| `account_not_verified` | 403 | `authorization_error` | 未实名账号领取体验积分 |
| `invalid_request_body` | 400 | `invalid_request_error` | 请求体不是合法 JSON、字段类型/长度/取值不合法 |
| `invalid_query_parameter` | 400 | `invalid_request_error` | `page`/`page_size`/`from`/`to`/`sort`/`order` 非法，审计筛选值超长 |
| `unknown_endpoint` | 404 | `invalid_request_error` | 未注册的 `/api/*` 路径 |
| `resource_not_found` | 404 | `invalid_request_error` | 运营后台按 id 寻址的行不存在或已删除 |
| `key_not_found` | 404 | `invalid_request_error` | 控制台不存在或不属于当前账号的 Key |
| `channel_not_found` | 404 | `invalid_request_error` | 运营后台渠道 id 不存在 |
| `channel_in_use` | 409 | `conflict_error` | 删除仍被路由引用的渠道 |
| `key_limit_reached` | 409 | `conflict_error` | 免费档活跃 Key 达到上限 |
| `resource_state_conflict` | 409 | `conflict_error` | 订单/退款/提额单/模型上架状态非法流转 |
| `trial_already_claimed` | 409 | `conflict_error` | 设备、手机、IP 段或账号已领取体验积分 |
| `invoice_already_issued` | 409 | `conflict_error` | 同一订单重复开票 |
| `price_conflict` | 409 | `conflict_error` | 同一模型存在多处不一致的价格来源 |
| `request_in_flight` | 409 | `conflict_error` | 相同幂等键的首次请求仍在处理 |
| `config_value_invalid` | 400 | `invalid_request_error` | 运行时配置值不符合命名空间/类型/范围 |
| `config_unavailable` | 503 | `service_unavailable_error` | 配置快照缺失或发布失败 |
| `auth_unavailable` | 503 | `service_unavailable_error` | Redis 不可用导致会话或登录计数无法读写 |
| `login_locked` | 429 | `rate_limit_exceeded` | 账号密码失败次数达到阈值后的临时锁定；`retry_after_seconds`/`reset_at` 为剩余锁定秒数与结束时刻 |
| `rpm_limit_exceeded` | 429 | `rate_limit_exceeded` | 同一 IP 的登录失败次数达到阈值；锁定期内该地址下的所有账号都返回此码 |
| `internal_error` | 500 | `internal_error` | 未预期错误；响应体不含堆栈 |

`rate_limit_exceeded` 是 error **type**，不是 76 码表里的 code。登录的两个失败预算按维度分码：

| 维度 | 阈值 | Redis 键 | code | 客户端应做的事 |
| :--- | :--- | :--- | :--- | :--- |
| 账号 | 5 次 / 15 分钟 | K28 `auth:fail:acct:{sha256(identifier)}` | `login_locked` | 等锁定结束；忘记密码走找回（待开放） |
| IP | 20 次 / 10 分钟 | K29 `auth:fail:ip:{sha256(ip)}` | `rpm_limit_exceeded` | 等待后重试，或更换网络 |

两个维度同时触发时按剩余 TTL 较长者返回。账号锁定不能再借用 `rpm_limit_exceeded`：该码表达「请求频率超限」，客户端会降频重试，而账号锁定期间即使密码正确也继续 429。

## 会话与 CSRF

| 项 | 值 |
| :--- | :--- |
| 会话 Cookie | `sid`，值 `{sid}.{iat}.{HMAC-SHA256(secret, sid|iat)}` |
| Cookie 属性 | `HttpOnly`、`Secure`、`SameSite=Lax`、`Path=/`、`Max-Age=8h` |
| CSRF Cookie | `csrf`，值 `hex(HMAC-SHA256(SESSION_SECRET, "csrf|" + sid))` |
| CSRF Cookie 属性 | **非 HttpOnly**、`Secure`、`SameSite=Lax`、`Path=/`、`Max-Age=8h` |
| CSRF 请求头 | `X-CSRF-Token`，值必须等于 `csrf` cookie，且等于服务端按当前 sid 重新计算的值 |
| Redis 会话键 | K20 `sess:admin:{sid}` → uid，TTL 2h 滑动 |
| Redis 会话索引 | K30 `sess:admin:uid:{uid}` → SET(sid)，TTL 与 K20 同步 |
| 滑动续期上限 | 8h（从签发时刻算，不随请求延长） |
| 登录成功 | 清空 K28 账号失败计数；写 K20 + K30；下发 `sid` 与 `csrf`；响应体带 `csrf_token` |
| 注册成功 | 同登录成功 |
| `GET /api/v1/auth/me` | 重新下发 `csrf` cookie；响应头 `X-CSRF-Token` 与响应体 `csrf_token`（在 `CurrentUser` 字段之外追加）返回同一 token |
| 登出 | 删除 K20、从 K30 `SREM`、清除 `sid` 与 `csrf` cookie；旧 token 随旧 sid 一起失效 |
| 改密 | `POST /api/v1/auth/password`，验证旧密码后更新 bcrypt 哈希，删除该 uid 的全部 K20 与 K30；旧 sid 再请求返回 401 `session_expired` |

CSRF 校验只作用于**会话鉴权**的状态变更方法：`POST` / `PATCH` / `PUT` / `DELETE`，且路径以 `/api/` 开头。豁免：

| 路径 | 原因 |
| :--- | :--- |
| `POST /api/v1/auth/login` | 无会话；token 无从派生 |
| `POST /api/v1/auth/register` | 无会话；token 无从派生 |
| `POST /api/v1/pay/callback/*` | 公开回调，认证靠请求体 HMAC，不依赖浏览器会话 |

CSRF 失败返回 403 `permission_denied`，不是 400 `invalid_request_body`：请求体本身没有错，改 body 也修不好；缺的是“请求来自本站”的证明。客户端处理方式是刷新页面重新获取 `csrf` cookie，并把当前 token 放进 `X-CSRF-Token`。
