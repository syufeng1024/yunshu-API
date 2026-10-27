# T14 · 用量·账单·请求日志 API

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T14 |
| 阶段 | 4 · 控制面 |
| 优先级 | P1 |
| 依赖 | T11、T09（日志已落库）、**T10**（错误码） |
| 可并行 | 与 T12 / T13 / T15 并行 |
| 交付根 | `internal/control/usage/`、`internal/control/logq/` |
| 状态 | 🟨 第一段已交付（2026-10-27）· 日志域完成；用量与账单未派 |

## 1. 目标

实现用户侧「钱花在哪、什么时候花的、每次请求发生了什么」的可查证能力：用量与账单（按模型/按渠道/按时间）、月度账单、CSV 导出、请求日志（含重试时间线）、正文查看与保留期边界。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| PRD §5.5 | 598–615 | 用量与账单：时间范围、按模型/按渠道切换、筛选保持、月度账单（积分 → 元 ÷100）、CSV 导出 |
| PRD §5.7 | 643–687 | 请求日志：状态/模型筛选、详情时间线、正文保留 180 天 / 元数据 365 天、`full_log_enabled` |
| PRD D.3 | 1702–1715 | F9-1–F9-3、F11-1–F11-3 验收路径 |
| PRD §9.6 / §10.2 | 1083–1096、1214–1229 | 数据基线与个人信息 |
| TDS §1.6 | 2419–2610 | `t_usage_record` / `t_upstream_attempt` 两级日志 |
| TDS §1.7 | 2610–2698 | 四张指标表与「实时值从日志重算」 |
| TDS §8.5 R-3/R-4 | 4835–4887 | **不做跨库 join**、维度冗余进日志行、365 天在线窗口 |

## 3. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `internal/control/usage/query.go` | `GET /api/console/usage`：时间范围（≤ 365 天）、维度（模型 / 渠道 / 日）、指标（请求数、token、积分、成功率） |
| 2 | `internal/control/usage/query.go` | 维度切换**保持其余筛选条件**（F9-1）；数据按新维度重算，不是前端二次聚合 |
| 3 | `internal/control/usage/bill.go` | `GET /api/console/bills?month=YYYY-MM`：自然月账单；积分 ÷100 → 元，展示 2 位小数；分账展示（充值/赠送各自消耗） |
| 4 | `internal/control/usage/export.go` | `POST /api/console/usage/export`：异步生成 CSV → 对象存储 → 返回下载链接（签名 URL，有效期 1h）；**列与页面一致、只含筛选内数据** |
| 5 | `internal/control/logq/list.go` | `GET /api/console/logs`：状态（成功/失败/限流）+ 模型 + 时间筛选（交集）；分页上限 100 |
| 6 | `internal/control/logq/detail.go` | `GET /api/console/logs/{request_id}`：请求元数据 + **重试时间线**（每次 attempt 的耗时与结果） |
| 7 | `internal/control/logq/body.go` | `GET /api/console/logs/{request_id}/body`：正文仅在保留期内且 `full_log_enabled` 时返回；否则 404 + 说明「正文已过保留期或未开启」 |
| 8 | `internal/control/logq/picker.go` | 日志选择器辅助：只列**90 天内本人**请求（PRD F19-1 的工单关联需求）；越权 `request_id` → 拒绝 |
| 9 | `internal/control/usage/metrics.go` | 实时值读 K14/K15；历史值查 `t_metric_*`；两者不可混用于同一图表（标注数据来源与延迟） |
| 10 | `internal/control/usage/*_test.go` | F9-1–F9-3、F11-1–F11-3 全覆盖 + 越权 + 保留期边界（180/365/366） |
| 11 | `api/openapi.yaml`（追加） | 本卡接口 schema |
| 12 | `docs/log-retention.md` | 正文/元数据/在线窗口三者的边界与越界行为（运维与客服共用） |

## 4. 硬约束（违反即驳回）

1. **不做跨库 join**：日志库与主库分离，查询所需维度（模型名、Key 名、渠道名）**冗余进日志行**，查询时不再 join。
2. **365 天是在线查询窗口，不是保留窗口**：查询接口硬限 `created_at >= now()-365d`，越界返回 400 并提示「请使用归档导出」。
3. **正文 180 天 / 元数据 365 天**：正文越界一律 404，**不得**返回空串伪装成功。
4. **导出必须异步 + 限流**：单用户同时最多 1 个导出任务；导出文件 1h 后失效；**不得**同步生成大 CSV。
5. **导出只含筛选内数据**，列与页面一致（同一份列定义常量，禁止两处各写一遍）。
6. **失败请求不计入用量统计口径**（PRD §3.1：不计费、不计入用量排行），但**要出现在日志列表**（带失败状态）。两张表的过滤条件不同，必须分别实现。
7. **账单金额换算唯一**：积分 ÷ 100，保留 2 位小数，四舍五入规则固定；**不得**用浮点累加。
8. **实时与历史不混用**：同一响应里若同时含实时值与历史值，必须分别标注字段（`realtime_*` / `settled_*`），并在 UI 说明延迟。
9. **越权隔离**：所有查询强制 `user_id = session.uid`；**不得**通过 `request_id` 反查他人数据。
10. **正文脱敏**：返回正文前按 `log.body_mode` 与用户设置判断；含敏感字段（手机号/邮箱/密钥样式字符串）须脱敏。
11. **排序默认 `created_at DESC`**，且必须命中 `idx_ur_created`；`EXPLAIN` 出现 `ALL` 即驳回。
12. **不得提供"删除日志"接口**（合规要求留存）；用户注销走 PRD §10.3 的独立流程。

## 5. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `go test ./internal/control/usage/... ./internal/control/logq/... -race` 全绿 | 复跑 |
| A2 | F9-1：近 7 日 + 模型 X → 切「按渠道」→ 筛选条件保持、数据按渠道重算 | 单测 |
| A3 | F9-2：月消费 12345 积分 → 显示 123.45 元 | 单测 |
| A4 | F9-3：CSV 仅含筛选内数据，列与页面一致（列名集合断言相等） | 单测 |
| A5 | F11-1：状态 + 模型筛选返回交集 | 单测 |
| A6 | F11-2：重试 1 次的请求 → 时间线 2 条，各自耗时与结果 | 单测 |
| A7 | F11-3：366 天前元数据在、181 天前正文已删（正文接口 404） | 单测 + 任务 |
| A8 | 越权：A 用自己的会话请求 B 的 `request_id` → 403/404 | 单测 |
| A9 | 365 天边界：`from=366 天前` → 400 + 提示归档 | 单测 |
| A10 | `EXPLAIN` 日志与用量查询均命中索引（无 `ALL`） | 手工 |
| A11 | 导出：并发发起 2 次 → 第二次 409/429；文件 1h 后失效 | 单测 |
| A12 | 失败请求：出现在日志列表、不出现在用量与账单 | 单测 |

## 6. 自检命令

```bash
go test ./internal/control/usage/... ./internal/control/logq/... -race -cover
M() { docker exec -i $(docker compose -f deploy/docker-compose.yml ps -q mysql) mysql -uroot -proot yunshu_log -N -e "$1"; }
M "EXPLAIN SELECT * FROM t_usage_record WHERE user_id=1 AND created_at>='2026-10-01' ORDER BY created_at DESC LIMIT 20"
```

## 7. 禁止事项

- 不得跨库 `JOIN`（日志库与主库不同 schema/实例）。
- 不得同步生成导出文件。
- 不得为"数据完整"去掉保留期限制。
- 不得提供删除日志的接口。
- 不得在前端做二次聚合（后端按维度返回）。

## 8. 完成回执

```
卡号: T14
接口清单: 路径 / 方法 / 对应 F 编号
保留期边界: A7/A9 输出
导出: A11 输出（异步证据 + 失效）
索引: EXPLAIN 结果
越权: A8 输出
F 覆盖: F9-1..F9-3、F11-1..F11-3 → 用例名
遗留:
```
