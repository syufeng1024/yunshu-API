# 请求日志与用量的保留期边界

请求正文保留 180 天、请求元数据保留 365 天、在线查询窗口 365 天，三者不是同一个数字。混淆任意两个会让客服承诺错数据，或让运维误判“数据已删除”。

## 三种期限

| 对象 | 存储 | 保留期 | 在线可查范围 | 越界返回 |
| :--- | :--- | :--- | :--- | :--- |
| 请求正文 | `t_request_body`（T22 migration `0022` 建表） | 180 天 | 正文接口自身按 180 天判定 | `404 resource_not_found`，message「正文已过保留期或未开启」 |
| 请求元数据 | `t_usage_record`、`t_upstream_attempt` | 365 天 | 范围查询 `from >= now()-365d` | `400 invalid_query_parameter`，hint「请使用归档导出」 |
| 用量 | `t_usage_record`、`t_metric_daily` | 与元数据同源 | 范围查询 `from >= now()-365d` | `400 invalid_query_parameter`，hint「请使用归档导出」 |
| 账单 | `t_usage_record` | 与元数据同源 | 按 `month=YYYY-MM` 查 CST 自然月，不做 365 天窗口校验 | 早于清理范围的月份返回 0；更早数据走归档流程 |
| 导出文件 | `EXPORT_DIR`（默认 `os.TempDir()/yunshu-exports`） | 签名令牌 1 小时 | 令牌内含 `exp = 创建时刻 + 1h` | `404 resource_not_found`，hint「导出链接有效期为 1 小时」 |

## 边界判定

| 场景 | 判定点 | 结果 |
| :--- | :--- | :--- |
| 正文：`created_at >= now()-180d` | `GET /api/console/logs/{request_id}/body` | 200（前提 `full_log_saved=1`） |
| 正文：`created_at < now()-180d` | 同上 | 404，不返回空串 |
| 正文：`full_log_saved=0` | 同上 | 404；请求从未落正文，与过期不可区分 |
| 元数据：`from = now()-366d` 的范围查询 | `GET /api/console/logs`、`GET /api/console/usage` 的 `from` | 400 + 归档提示 |
| 元数据：366 天前行的精确查询 | `GET /api/console/logs/{request_id}` | 200；窗口只约束范围扫描，行是否还在由清理任务决定 |
| 导出：令牌 `exp <= now()` | `GET /api/console/exports/{token}` | 404；磁盘文件即使仍在也不可下载 |

`now()` 取服务注入的 UTC 时钟；正文判定用 `req.CreatedAt.Before(now.Add(-180*24h))`，等号当天仍可查。范围下界判定用 `from.Before(now.Add(-365*24h))`，恰好 365 天整可查，早 1 毫秒即拒绝。

## 各接口的过滤口径

| 接口 | 计费类指标（tokens/积分/金额） | 请求数与成功率 |
| :--- | :--- | :--- |
| `GET /api/console/logs` | 不适用 | 默认不过滤；失败请求必须可见，`state=failed` 只收窄，不改变默认 |
| `GET /api/console/logs/{request_id}` | 不适用 | 不过滤；单条请求详情，含失败与重试时间线 |
| `GET /api/console/usage` | 仅 `SUCCESS` | `request_total` / `request_success` / `success_rate_bp` 统计全部 `result` |
| `GET /api/console/bills` | 仅 `SUCCESS` | 同上，按 CST 自然月统计 |
| `POST /api/console/usage/export` | 仅 `SUCCESS` | CSV 的「请求数 / 成功数 / 成功率」按全部 `result`，其余列与用量页同口径 |

PRD §3.1 的「不计费、不计入用量排行」约束的是积分、token 与按 `consumed_micro DESC` 的排序，不约束成功率的分母。分母若只取 `SUCCESS`，`success_rate_bp` 恒为 10000，指标失去意义。实现上两者在**同一条聚合 SQL** 内完成：`COUNT(*)` 得到 `request_total`，`SUM(result='SUCCESS')` 得到 `request_success`，计费列用 `SUM(CASE WHEN result='SUCCESS' THEN ... END)`。`success_rate_bp = request_success * 10000 / request_total`，整数截断（2/3 → 6666），`request_total=0` 时为 0。

失败请求同时存在于日志列表与 `t_usage_record`（正常情况下 `consumed_micro=0`）。它计入用量页与账单的 `request_total` 与成功率分母，但不贡献 token、积分与金额，也不抬高按消耗积分排序的名次。日志列表的 SQL 与用量聚合的 SQL 分别写在 `internal/control/logq/list.go` 与 `internal/control/usage/query.go`，没有共用默认值。

## 超过 365 天怎么办

在线接口不提供更早数据，返回的 hint 指向归档导出。归档数据不经 `t_usage_record` 在线查询，也不受正文 180 天规则影响。客服答复“数据查不到”时，先确认是正文过期（180 天）还是范围越界（365 天）：前者换不了接口，后者需要走归档流程。

## 不删除

`t_usage_record` 与 `t_upstream_attempt` 没有删除接口（T14 硬约束 12）。账号注销不删除日志，按 PRD §10.3 的独立流程处理。清理任务只按保留期批量删除，不做单条删除。
