# 交付文件索引

生成方式：`git ls-files` 取全量 846 个跟踪文件，`wc -l` 取行数；目录行是**该目录下全部文件（含测试）的合计**，单文件行单独列出。职责描述按卡的实际交付根补写，不按 PRD 章节照抄。统计日期 2026-10-29。`docs/tasks/delivery/` 下 5 份为本轮新建、尚未纳入 Git 跟踪，故不计入 846 与 `docs/` 合计。

| 项 | 值 |
| :--- | :--- |
| 跟踪文件总数 | 846 |
| `internal/` | 450 文件 / 95,932 行 |
| `relaykit/` | 30 文件 / 5,526 行 |
| `cmd/` | 7 文件 / 1,842 行 |
| `web/src/` | 171 文件 / 36,947 行 |
| `docs/` | 55 文件 / 16,415 行 |
| `deploy/` | 25 文件 / 2,586 行 |
| `scripts/` | 13 文件 / 2,134 行 |
| `api/` | 1 文件 / 4,029 行 |

## 阶段 0–1 · 工程基线与数据层

| 卡 | 路径 | 行数 | 职责 |
| :--- | :--- | ---: | :--- |
| T01 | `go.mod` / `go.sum` | 19 / — | 模块声明与依赖锁定；`go.sum` 不列行数 |
| T01 | `Makefile` | 89 | `up/down/check/ci` 入口，Windows 下由 `scripts/dev.sh` 等价执行 |
| T01 | `.env.example` | 68 | 全部环境变量模板；新增变量必须同步此文件 |
| T01 | `.gitattributes` / `.gitignore` / `.dockerignore` | 25 / 38 / 31 | 行尾归一（SQL/Lua 强制 LF）、忽略规则、构建上下文裁剪 |
| T01 | `.golangci.yml` / `.trivyignore` | 17 / 6 | 静态检查配置、CVE 例外清单 |
| T01 | `cmd/controld/main.go` | 311 | 控制面进程入口：配置启动检查、迁移校验、HTTP 监听、优雅退出 |
| T01 | `cmd/gatewayd/main.go` | 482 | 网关进程入口：快照装载、限流/熔断、P01–P25 流水线装配 |
| T01 | `cmd/jobsd/main.go` | 286 | 后台任务进程：结算 worker、日志清理、日对账、租约选主 |
| T01 | `cmd/keytool/main.go` | 156 | 上游 Key 信封加解密与 `seed-channel` 工具 |
| T01 | `cmd/migrate/main.go` | 129 | `migrate up/down/status` CLI |
| T01 | `cmd/seedctl/main.go` | 419 | 本地种子：用户、Key、渠道、模型、路由、价目 |
| T01 | `cmd/fakeupstream/main.go` | 59 | 假上游，供 A5/重试用例构造 DNS/TLS/5xx |
| T01 | `internal/platform/clock` | 33 | 全仓唯一 `time.Now` 入口，返回 UTC |
| T01 | `internal/platform/db` | 104 | MySQL 连接池与事务辅助 |
| T01 | `internal/platform/env` | 296 | 环境变量解析与必填校验 |
| T01 | `internal/platform/httpsrv` | 870 | HTTP 中间件：请求 ID、traceparent、恢复、超时 |
| T01 | `internal/platform/redis` | 40 | Redis 客户端构造与 `noeviction` 自检 |
| T01 | `deploy/docker-compose.yml` / `docker-compose.portmap.yml` | 45 / 26 | MySQL 8.0 + Redis 7 本地栈与端口映射覆盖 |
| T01 | `scripts/dev.sh` | 186 | Makefile 的跨平台等价入口 |
| T01 | `scripts/test-env.sh` | 60 | 测试库/Redis 环境变量注入 |
| T02/T03 | `internal/platform/migrate` | 3,271 | 迁移引擎、embed、checksum 守卫、编号唯一性断言 |
| T02/T03 | `deploy/mysql/init/00-grants.sql` | 15 | 建库与最小权限账号 |
| T03 | `internal/platform/migrate/sql/mysql/0018_seed_config.sql` | 143 | 45 个配置键种子（PRICING/UPSTREAM/FEATURE/RATELIMIT） |
| T03 | `internal/platform/migrate/sql/mysql/0019_seed_local.sql` | 132 | `ENV=local` 专用种子（渠道/模型/路由/价目） |
| T02/T03 | `internal/platform/migrate/sql/mysql/0001–0025` | 含于上 | 25 组 up/down；`0007`/`0008` 永久空置，`0013` 不可回滚 |

## 阶段 2 · 核心域

| 卡 | 路径 | 行数 | 职责 |
| :--- | :--- | ---: | :--- |
| T04 | `internal/ledger` | 5,807 | 账本：批次、预扣、结算、冲正、退款冻结、余额 CAS |
| T04 | `internal/billing` | 1,768 | 计费内核：唯一舍入点、预估算、阶梯价、安全系数 |
| T04 | `internal/balance` | 283 | 账户可用余额与欠费状态读取 |
| T05 | `internal/config` | 5,103 | 配置快照、策略注册表、K13/K19/K26、版本发布与失效通道 |
| T06 | `internal/redisx` | 1,188 | K1–K26 键构造、Lua 脚本、租约 |
| T06 | `internal/ratelimit` | 2,035 | 四维限流：并发/RPM/TPM/日额度，判定与报码分离 |
| T06 | `internal/lock` | 190 | Redis 分布式锁 |
| T06 | `internal/idemstore` | 1,602 | 幂等记录 MySQL 适配器、fence 租约 |
| T06 | `internal/breaker` | 550 | 渠道熔断与半开探测 |

## 阶段 3 · 数据面

| 卡 | 路径 | 行数 | 职责 |
| :--- | :--- | ---: | :--- |
| T07 | `internal/gw` | 6,599 | P01–P14：鉴权、校验、模型、限流、估算、预扣、选路、错误出口 |
| T08 | `relaykit`（独立 module） | 5,526 | OpenAI / Anthropic / 百炼三协议编解码与统一 IR |
| T08 | `internal/dispatch` | 3,106 | 上游派发、重试矩阵、SSE 转发、错误映射、Key 解封 |
| T08 | `internal/stream` | 1,223 | SSE 分帧、半帧拼接、`[DONE]`、首 token 判定 |
| T08 | `internal/cryptobox` | 272 | `v1:` 信封加解密（DEK + KEK） |
| T09 | `internal/settle` | 2,625 | 结算 worker、用量落库、正文异步写、悬挂恢复 |
| T09 | `internal/recon` | 3,434 | 日对账、R01–R08 内部一致性、8 条对账断言 |
| T09 | `internal/jobs` | 2,813 | 日志清理、四表 rollup、任务锁 |
| T10 | `internal/apierr` | 3,459 | 76 个错误码 spec、唯一写出口、429/402 头体契约、SSE 错误帧 |
| T10 | `internal/jsonx` | 19 | 全仓唯一 `encoding/json` 封装 |

## 阶段 4 · 控制面

| 卡 | 路径 | 行数 | 职责 |
| :--- | :--- | ---: | :--- |
| T11 | `internal/control/server.go` | 456 | 控制面路由装配与权限点绑定 |
| T11 | `internal/control/server_authz_test.go` | 438 | 13+ 条 `/api/admin/*` 对 `PERSONAL` 全 403 的越权矩阵 |
| T11 | `internal/control/mw` | 1,212 | 会话、CSRF 双提交、鉴权、分页、恢复 |
| T11 | `internal/control/auth` | 1,932 | 登录/注册/改密/注销、限流与账号锁定、踢出会话 |
| T11 | `internal/control/audit` | 148 | 审计写入唯一入口 |
| T11 | `internal/control/auditq` | 712 | 审计查询、target 过滤 |
| T11 | `internal/control/ctltest` | 139 | 控制面测试 harness（独立 schema + Redis） |
| T11 | `internal/control/plan` | 131 | 套餐读取与权益映射 |
| T11 | `internal/apikey` | 164 | Key 生成（30 字节 → 48 字符 Crockford）、SHA256 存储 |
| T11 | `internal/boot` | 873 | ROUTING 快照与 K13/K19 生产方、`Publish` 链路 |
| T12 | `internal/control/key` | 3,505 | 用户密钥 CRUD、批量创建、轮换、停用、审计、实时指标 |
| T12 | `internal/control/user` | 598 | 概览聚合、24h 曲线 |
| T12 | `internal/control/model` | 742 | 控制台模型广场、上下文、价格换算、排序 |
| T13 | `internal/control/fund` | 7,370 | 订单、回调幂等、退款、发票、提额、兑换码、体验金、通知 |
| T14 | `internal/control/usage` | 2,487 | 用量查询、月度账单、异步 CSV 导出、指标 |
| T14 | `internal/control/logq` | 1,904 | 请求日志列表/详情/时间线、正文解密与脱敏 |
| T15 | `internal/control/ops` | 11,126 | 渠道、定价、分组、公告、风控、用户、配置、运营概览 |
| T15 | `internal/control/publicapi` | 894 | 公开模型/套餐/公告/接入点（接入点为刻意空桩） |
| T16 | `internal/control/ticket` | 3,718 | 工单创建/列表/详情/回复/SLA/注销阻断/请求选择器 |
| T16 | `internal/control/review` | 2,893 | 审核中心五类单据、升序队列、驳回必填、自审拒绝、余额调整 |
| T24 | `internal/control/console` | 1,026 | 控制台聚合接口与调试台内网转发 |
| T24 | `internal/control/units` | 92 | 微积分/积分/元/分换算 |
| T24 | `api/openapi.yaml` | 4,029 | 控制面与公开接口契约 |

## 阶段 5 · 前端

| 卡 | 路径 | 行数 | 职责 |
| :--- | :--- | ---: | :--- |
| T17 | `web/package.json` / `package-lock.json` | 47 / — | 依赖与脚本；锁文件不列行数 |
| T17 | `web/vite.config.ts` | 114 | 构建配置 + `assertNoPricingLiterals` / `assertNoFixtures` 两道门禁 |
| T17 | `web/vitest.config.ts` / `web/playwright.config.ts` | 19 / 98 | 单测与 e2e 配置 |
| T17 | `web/tailwind.config.ts` / `postcss.config.js` | 110 / 6 | 设计 token 到 Tailwind 的映射 |
| T17 | `web/tsconfig.json` / `web/index.html` / `web/.env.example` | 31 / 20 / 15 | TS 配置、入口 HTML、前端环境变量 |
| T17 | `web/src/styles` | 189 | `tokens.css` 与全局样式，无裸色值 |
| T17 | `web/src/components`（顶层公共组件） | 1,905 | Table / Modal / CopyBlock / Chart 等自研组件 |
| T17 | `web/src/lib` | 1,415 | 格式化、SSE 解析、深链、i18n、防抖、剪贴板 |
| T17 | `web/src/hooks` / `web/src/stores` / `web/src/test` | 207 / 111 / 53 | 实时指标轮询、全局状态、测试工具 |
| T17 | `web/src/App.tsx` / `main.tsx` / `vite-env.d.ts` | 18 / 15 / 10 | 应用根、挂载入口、Vite 类型 |
| T17 | `web/src/router.tsx` / `web/src/router/guards.tsx` | 84 / 103 | 22 条路由表、`RequireAuth` / 角色守卫 |
| T18 | `web/src/pages/site` | 2,178 | 官网 5 页：首页、定价、文档、登录、集成 |
| T18 | `web/src/components/site` | 1,163 | 价格表、套餐卡、复制块、推荐位、接入点、深链按钮 |
| T18 | `web/src/content` | 468 | 工具集成与文档章节的单一内容源 |
| T18–T20 | `web/src/fixtures` | 804 | 开发态预览数据；生产构建被 `assertNoFixtures` 剔除 |
| T19 | `web/src/pages/console` | 6,430 | 控制台 9 页：概览、密钥、详情、广场、用量、充值、日志、调试台、工单 |
| T19 | `web/src/components/console` | 2,616 | 创建向导、密钥一次性展示、账单、图表、充值/提额弹窗 |
| T20 | `web/src/pages/admin` | 8,246 | 运营后台 8 页：概览、渠道、用户、定价、公告、审核、工单、模型 |
| T20 | `web/src/components/admin` | 735 | 审计轨迹、状态徽标、筛选与表单控件 |
| T20 | `web/src/api` | 9,833 | 契约类型与调用层；含 `types.ts` 生成代码 5,911 行 |
| T29 | `web/src/layouts` | 364 | 官网 / 控制台 / 后台三套 layout 与滚动复位 |
| T17 | `web/scripts/check-copy.mjs` | 55 | 对外文案禁用词扫描 |

## 阶段 6 · 质量、交付与运维

| 卡 | 路径 | 行数 | 职责 |
| :--- | :--- | ---: | :--- |
| T21 | `internal/acceptance/ci14_test.go` | 778 | 14 条必进 CI 用例（F4-2 / F7-1 / F7-4 / F14-2 / F19-1 / F6-7 / F11-3 / F14-4 / F14-5 / F14-6 / F16-4 / F16-6 / F19-4 / F20-3） |
| T21 | `internal/acceptance/gwt_test.go` | 1,370 | G-01–G-12 的 12 条 GWT |
| T21 | `internal/acceptance/main_test.go` | 550 | 独立 schema 建库、迁移、Redis 隔离 |
| T21 | `internal/acceptance/matrix_test.go` | 140 | 验收矩阵编号/行数守卫 |
| T30 | `internal/acceptance/g11_publish_test.go` | 143 | 真 `Publish` → `cfg:ver` N→N+1 的集成断言 |
| T21 | `internal/load` | 449 | 200 QPS 长跑（`-tags soak`） |
| T21 | `web/e2e` | 275 | Playwright：跨用户 Key、Key 明文、请求选择器 |
| T21 | `.github/workflows` | 635 | CI（static/unit/migrations/coverage/e2e/acceptance）、nightly、release |
| T21 | `scripts/coverage-gate.sh` / `e2e-backend.sh` / `test-gwt.sh` | 116 / 67 / 14 | 覆盖率门禁、e2e 后端启动、GWT 一键跑 |
| T22 | `internal/obs` | 2,822 | 结构化日志（traceparent）、掩码、健康检查、指标 Registry |
| T22 | `deploy/Dockerfile.app/controld/gatewayd/jobsd/web` | 80 / 41 / 41 / 42 / 41 | 五个非 root 镜像，单二进制 |
| T22 | `deploy/k8s` | 565 | configmap/secret 示例、三进程 Deployment、canary 池 |
| T22 | `deploy/nginx` | 198 | 站点与网关分流、`/internal/` 拦截 |
| T22 | `deploy/docker-compose.stack.yml` / `.debug.yml` | 238 / 28 | 全栈编排与调试覆盖 |
| T22 | `deploy/.env.stack.example` / `deploy/README.md` | 119 / 339 | 全栈环境变量模板与部署说明 |
| T22 | `scripts/deploy.sh` / `rollback.sh` / `smoke.sh` / `stack.sh` | 349 / 150 / 325 / 199 | 发布、回滚、冒烟、栈管理 |
| T25 | `internal/platform/migrate/sql/mysql/0022_request_body.sql` | 29 | `t_request_body` 7 列 + 3 索引 |
| T25 | `internal/logq` | 225 | 正文信封 KEK 解密与读取端 |
| T26 | `scripts/check-eol.sh` / `gen-migration-checksums.sh` | 58 / 11 | CRLF 守卫、迁移 checksum 重生成 |
| T26 | `scripts/lint.sh` | 112 | 静态门禁总入口，`check-eol.sh` 为其中第 10 条 |
| T28 | `deploy/seed` | 768 | 平台/额度/用量/渠道/汇总种子 SQL |
| T28 | `scripts/seed-demo.sh` | 487 | 空卷重建 → 播种 → 渠道加密的演示链路 |

## 文档

| 卡 | 路径 | 行数 | 职责 |
| :--- | :--- | ---: | :--- |
| — | `docs/_API 中转站 · 需求文档 V1.2.md` | 2,306 | PRD：需求、GWT、82 项、65 码 |
| — | `docs/技术架构设计文档 V1.2.md` | 4,959 | TDS：架构、DDL、错误码、迁移编号表 |
| — | `docs/schema.md` | 162 | 表清单与迁移编号登记 |
| — | `docs/config-keys.md` | 92 | 45 个配置键与命名空间归属 |
| — | `docs/control-api.md` | 377 | 控制面接口契约 |
| — | `docs/error-codes.md` | 95 | 错误码表（由 `MarkdownTable()` 生成） |
| — | `docs/finance-rules.md` / `fund-flow.md` | 47 / 159 | 计费规则、资金与退款/开票流转 |
| — | `docs/settlement.md` / `redis-keys.md` | 93 / 63 | 结算与对账、Redis 键空间 |
| — | `docs/log-retention.md` / `relay-contract.md` | 48 / 128 | 日志保留双窗口、协议转换契约 |
| — | `docs/ops-runbook.md` / `dev-setup.md` | 151 / 175 | 运维手册、本地开发与行尾说明 |
| — | `docs/test-matrix.md` | 115 | 测试矩阵与已知偏差 |
| T23 | `docs/tasks/acceptance/matrix-82.md` | 95 | 82 项功能点对照（✅66/⚠️14/❌2） |
| T23 | `docs/tasks/acceptance/matrix-gwt.md` | 27 | 12 条 GWT 对照（✅5/⚠️7） |
| T23 | `docs/tasks/acceptance/matrix-pages.md` | 39 | 22 页路由与 200/401/403 实测 |
| T23 | `docs/tasks/acceptance/matrix-codes.md` | 108 | PRD 65 码 + 实现新增 11 码附录 |
| T23 | `docs/tasks/delivery/deviations.md` | 47 | 30 条偏差登记（14 + 7 + 9） |
| T23 | `docs/tasks/delivery/errata.md` | 26 | 8 条源文档勘误与本轮回写清单 |
| T23 | `docs/tasks/delivery/open-issues.md` | 31 | 12 条未解决项与阻塞判断 |
| T23 | `docs/tasks/delivery/go-live.md` | 55 | 八条门槛逐条判定与 No-Go 结论 |
| T23 | `docs/tasks/delivery/file-index.md` | 本文件 | 全量交付文件索引 |
| — | `docs/tasks/README.md` | 805 | 任务卡总览、决策 D-01–D-25、验收记录与台账 |
| — | `docs/tasks/BACKLOG.md` | 51 | 跨卡待办 G1–G14 与上线前必办 |
| — | `docs/tasks/HANDOFF-2026-10-28.md` | 116 | 交接说明 |
| — | `docs/tasks/errata.md` | 55 | 源文档勘误 E-01–E-34（实现期累计） |
| — | `docs/tasks/ref-frontend-structure.md` | 102 | 前端结构参考（参考非规范） |
| — | `docs/tasks/T01`–`T30` 任务卡 | 6,047 | 31 个文件（30 张卡 + T27 两段）的边界、硬约束、验收标准与回执 |

## 部署与脚本（逐文件）

| 卡 | 路径 | 行数 | 职责 |
| :--- | :--- | ---: | :--- |
| T22 | `deploy/Dockerfile.app` | 80 | 控制面/后台静态产物镜像基座 |
| T22 | `deploy/Dockerfile.controld` / `gatewayd` / `jobsd` | 41 / 41 / 42 | 三个 Go 进程的独立镜像 |
| T22 | `deploy/Dockerfile.web` | 41 | 站点镜像（nginx + 静态产物） |
| T22 | `deploy/k8s/canary/gateway-canary.yaml` | 140 | 灰度池 Deployment + Service |
| T22 | `deploy/k8s/configmap.yaml` / `secret.example.yaml` | 18 / 18 | 非密钥配置与密钥模板 |
| T22 | `deploy/k8s/controld.yaml` / `gatewayd.yaml` / `jobsd.yaml` | 129 / 173 / 87 | 三进程资源、探针、副本策略 |
| T22 | `deploy/nginx/default.conf` / `gateway.conf` | 121 / 77 | 站点 SPA 回退、网关与 `/internal/` 拦截 |
| T28 | `deploy/seed/01-platform.sql` | 126 | 平台基础数据 |
| T28 | `deploy/seed/02-credit.sql` | 270 | 账户/额度/套餐 |
| T28 | `deploy/seed/03-usage.sql` | 167 | 模型/价格/用量样例 |
| T28 | `deploy/seed/04-channel.sql` | 99 | 渠道与路由（`v1:` 信封 Key） |
| T28 | `deploy/seed/99-summary.sql` | 106 | 播种后计数校验 |
| T26 | `scripts/check-eol.sh` | 58 | 46 个 SQL + checksums.golden 的 CR 检测 |
| T26 | `scripts/gen-migration-checksums.sh` | 11 | 重生 `checksums.golden` |
| T25/T21 | `scripts/coverage-gate.sh` | 116 | ledger/billing/apierr/整体四条覆盖率阈值 |
| T21 | `scripts/e2e-backend.sh` | 67 | Playwright 前置：独立库 + 服务 |
| T21 | `scripts/test-gwt.sh` | 14 | 12 条 GWT 一键执行 |
| T22 | `scripts/deploy.sh` | 349 | 构建、推送、滚动发布、`--dry-run` |
| T22 | `scripts/rollback.sh` | 150 | 镜像与配置回滚 |
| T22 | `scripts/smoke.sh` | 325 | 发布后冒烟（健康检查、关键接口） |
| T22 | `scripts/stack.sh` | 199 | 全栈 compose 启停与状态 |
| T28 | `scripts/seed-demo.sh` | 487 | 空卷重建、播种、渠道 Key 加密 |
| T01 | `scripts/dev.sh` | 186 | 本地开发入口（Makefile 等价） |
| T01 | `scripts/test-env.sh` | 60 | 测试环境变量注入 |
