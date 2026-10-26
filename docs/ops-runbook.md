# 对账告警值班手册

`jobsd` 的 `daily_reconcile` 每日 03:00（UTC+8）跑前一自然日，差异落 `t_reconcile_diff`；P0 与 CRITICAL 告警落 `t_notification`。值班从告警接手到关闭差异的全部动作在下文，任何一步都不允许直接改余额或删流水。

## 1 告警入口与时限

| event_code | 触发判据（代码位置） | channel_mask | 时限 | 接手人 |
| --- | --- | --- | --- | --- |
| `RECONCILE_P0` | `runExternal`：单笔 `|delta_micro| > 1_000_000`（1 元）、当日 `sum_abs_delta_micro > 100_000_000`（100 元）、或 `MISSING_LOCAL` 笔数 > 0 | 7（站内信+邮件+短信） | **15 分钟**内电话确认接单 | 研发值班 + 财务 + SUPER_ADMIN |
| `RECONCILE_ALERT` | R-01/R-02/R-03 任一行，或 R-05 偏差 > 10,000 微积分 | 3（站内信+邮件） | 15 分钟（CRITICAL 即资损） | 研发值班 |

`channel_mask` 的 bit2 只发短信，仓库里没有外呼集成：**电话确认由值班手机/值班群完成**。15 分钟无人接单，由 SUPER_ADMIN 按值班表升级。HIGH/MEDIUM（R-04/R-06/R-07/R-08）只写 `ERROR`/`WARN` 日志，次日处理。

查看当前未关闭差异：

```sql
SELECT id, biz_date, diff_type, request_id, trade_no, local_value, upstream_value,
       delta_micro, match_mode, status, detected_at
  FROM t_reconcile_diff
 WHERE status IN ('OPEN','HANDLING')
 ORDER BY ABS(delta_micro) DESC, detected_at
 LIMIT 100;
```

## 2 接单：OPEN → HANDLING

拿到责任后立刻把行置为 `HANDLING`，防止第二个值班重复定位。仓库当前没有修改状态的管理接口（属 T13/T15），用受限运维账号执行：

```sql
UPDATE t_reconcile_diff
   SET status = 'HANDLING', updated_at = NOW(3)
 WHERE id = ? AND status = 'OPEN';
-- 期望 Rows matched: 1；0 行说明别人已接单或已关闭，停止操作
```

同时写审计（`t_admin_audit_log`，`module='FINANCE'`）：

```sql
INSERT INTO t_admin_audit_log
  (trace_id, operator_id, operator_role, module, action, target_type, target_id, reason, result, created_at)
VALUES (?, ?, 'OPS', 'FINANCE', 'UPDATE', 't_reconcile_diff', ?, '对账差异接单', 'OK', NOW(3));
```

## 3 R-01 六步处置

R-01 是「`t_credit_account.balance_micro` ≠ `SUM(t_ledger_entry.amount_total_micro)`」，没有容差。

**第 1 步：判定单点还是系统性**

```sql
-- Q1 近 1 小时 OPEN 的 R-01 差异涉及多少用户（subject 即 user_id）
SELECT COUNT(DISTINCT subject) AS users
  FROM t_reconcile_diff
 WHERE status = 'OPEN' AND diff_type = 'R01_BALANCE_MISMATCH'
   AND detected_at > NOW() - INTERVAL 1 HOUR;
-- Q2 差异方向：正数 = 余额少于流水（余额被少记），负数反之
SELECT user_id, balance_micro, ledger_sum FROM (
  SELECT a.user_id, a.balance_micro, SUM(l.amount_total_micro) AS ledger_sum
    FROM t_credit_account a JOIN t_ledger_entry l ON l.user_id = a.user_id
   WHERE a.user_id = ? GROUP BY a.user_id, a.balance_micro
) x;
```

**第 2 步：定位断点**。R-01 的 `subject` 是 `user_id`（十进制字符串），差异行本身不带断点位置，按流水逐条比对：

```sql
SELECT id, entry_no, balance_after_micro, next_before FROM (
  SELECT id, entry_no, balance_after_micro,
         LEAD(balance_before_micro) OVER (ORDER BY id) AS next_before
    FROM t_ledger_entry WHERE user_id = ?
) x
WHERE next_before IS NOT NULL AND next_before <> balance_after_micro;
```

**第 3 步：冻结范围 = 只冻结提现/退款，不冻结调用**。TDS §5.4 原文的「冻结消费写路径」已被需求方 2026-10-23 裁定作废（F-11）：差异不影响新请求的正确性，新预扣走 `t_credit_account` 的 CAS。V1.0 的提现/退款尚未实现（T13），上线后须在出口校验该用户近 7 天是否存在 `status='OPEN'` 的差异（TDS §8.3.2 方案②，零迁移）。

**第 4 步：核对成因**。按经验顺序排查，命中即停：

```sql
-- 悬挂预扣：过期仍 HELD 的行
SELECT COUNT(*) FROM t_prehold_hold WHERE user_id = ? AND state = 'HELD' AND expires_at < NOW();
-- 结算任务放弃：重试耗尽转 DEAD
SELECT COUNT(*) FROM t_settle_task WHERE user_id = ? AND state = 'DEAD';
-- 批次口径：remaining ≠ face - consumed（R-04 同源）
SELECT id, batch_no, remaining_micro, face_value_micro, consumed_micro
  FROM t_credit_batch WHERE user_id = ? AND remaining_micro <> face_value_micro - consumed_micro;
```

成因排序：① `t_settle_task` 转 `DEAD`；② 预扣后进程崩溃（半完成态，`prehold_release` 应已回收，未回收说明 K12 锁 `hold:recover:{request_id}` 仍被占用）；③ Redis 释放失败且 `pending_release` 缓冲溢出；④ 库里存在直接 `UPDATE t_credit_account` 的代码（`TestProductionSQLGuardRails` 每次 CI 扫描，仍有命中即先停发布）；⑤ 当日迁移漏执行。

**第 5 步：冲正**。只允许追加，不允许改历史：

- 入口 `recon.NewReverser(db, clock.Real{}).Reverse(ctx, recon.ReverseParams{EntryNo, Reason, OperatorID})`；
- `Reason` ≥ 10 字，入库为 `REVERSAL_OF:<原 entry_no>:<原因>`；
- 全额冲正，`idem_key='reversal:<原 entry_no>'` 由 `uk_le_idem` 保证一笔原流水只冲一次；第二次调用返回同一分录（`Idempotent=true`），不是错误；
- 先由财务/SUPER_ADMIN 写 `action='APPROVE'` 的审计（`after_json` 带 `approved_reversal_micro`），执行人不得与审批人相同；执行时再写 `action='ADJUST_BALANCE'`、`before_json` 带审批记录 id。

**第 6 步：复核后关闭**。`Reverse` 返回前已执行 `Verify`（R-01、R-03、冲正自洽三条），任一条失败会返回 `reversal committed but post-check failed`——此时**停止**，升级人工介入，不得再冲一笔。

## 4 HANDLING → CLOSED 手工流

只有同时满足以下四条才能关闭：

1. 第 3 步的 R-01 查询对该用户返回 0 行；
2. `Reverser.Verify(原 entry_no)` 无错；
3. 冲正流水 `idem_key='reversal:<原 entry_no>'` 存在且 `amount_total_micro = -原金额`；
4. 审批审计（`action='APPROVE'`）与执行审计（`action='ADJUST_BALANCE'`）都在，且 operator 不同。

```sql
UPDATE t_reconcile_diff
   SET status = 'CLOSED', updated_at = NOW(3)
 WHERE id = ? AND status = 'HANDLING';
```

`t_reconcile_diff` 没有 `note` / `resolved_by` / `resolution` 列（0021 未建），TDS §8.3.4 要求的处置说明写到 `t_admin_audit_log.reason` 与 `after_json`，`target_id` 填 diff 的 `id`，`reason` 写「根因 + 冲正 entry_no + 处置耗时」。

关闭不等于数据已修正：R-01 的修正是冲正分录，`status` 只表示差异已定位。下一次 03:00 重跑时，`Recorder` 对同 `diff_key`、同 `delta_micro` 的行保持 `CLOSED` 不重开；`delta_micro` 一旦变化会自动置回 `OPEN` 并刷新 `detected_at`——**这是差异复发的信号，不要当成误报**。

## 5 误报判定

| 现象 | 判据 | 动作 |
| --- | --- | --- |
| 当日 `t_upstream_bill` 无行 | `runExternal` 对 0 账单日直接返回，不产生 `MISSING_UPSTREAM` | 无需处置；确认账单适配器是否漏拉，漏拉按 T08 渠道告警处理 |
| R-05 偏差 100~10,000 微积分 | 容忍带内（Redis 补偿失败、跨日结算写旧桶），HIGH 不告警 | 以 DB 为准重建当日计数：`SET rl:dq:{user_id}:{yyyymmdd} <db_value>`，次日重跑归零后关闭 |
| R-07 计数超阈值但删除任务已跑 | `t_idempotency_record` 中 `expires_at < NOW()` 的行确实在下降 | 关闭；若连续两日不降，按 `purge_log` 失败处理 |
| 同一差异在 `CLOSED` 后原样重复 | `delta_micro` 未变，状态仍是 `CLOSED` | 不是告警，忽略 |
| 差异由校验口径导致（阈值过紧、汇总键选错） | 手工 SQL 复算结果一致且无资金影响 | 关闭并在团队改校验代码或阈值；**禁止改历史数据来让断言变绿** |

误报与真实差异共用一张表，所以「删行」永远不是误报的处置方式；关闭后若 `delta_micro` 变化，行会重开。

## 6 禁止操作

| 禁止 | 原因 / 现有护栏 |
| --- | --- |
| `UPDATE t_credit_account SET balance_micro = ?` | 第二真相源；`TestProductionSQLGuardRails` 扫描全仓，命中即测试失败 |
| `UPDATE` / `DELETE t_ledger_entry` | append-only；同一测试断言 |
| `TRUNCATE` / `DROP PARTITION` 日志表 | 唯一键不含 `created_at`，分区会失去 `uk_ur_request` 全局唯一性；`purge_log` 用每批 5000 行的 DELETE |
| 手工 `INSERT INTO t_reconcile_diff` | 唯一生产者是 `recon.Recorder`，手插会算错 `diff_key=sha256(biz_date|diff_type|subject)` |
| 同一天并发跑两次 `daily_reconcile` | K21 `lock:recon:{yyyymmdd}` EX 7200 只放行一个副本，第二个 `Skipped=true` |
| 为「立即重跑」`DEL lock:recon:*` | 只读任务重跑不修数据，重复执行只会重复发通知；K21 过期（03:00 + 2h = 05:00）后再跑 |
| 用冻结 API 调用来「控制影响面」 | F-11 裁定：只冻结提现/退款 |

## 7 其余四个任务的失败处置

| 任务 | 失败日志 | 处置 |
| --- | --- | --- |
| `prehold_release`（60s） | `task_failed job=prehold_release` | 单轮失败不影响下一轮；连续 3 轮积压或 `hold:recover:{request_id}` 锁未释放，查 Redis 与 MySQL 连通性 |
| `expire_batch`（00:05） | `job_skipped_not_leader` / `task_failed` | 只处理已过期且 `remaining_micro > 0` 的 BONUS；重跑安全（`idem_key='expire:{batch_id}'`）。RECHARGE 批次永不选入 |
| `purge_log`（03:10） | `Truncated=true` | 达到 100 批上限，次日自动续删；不要手工 `TRUNCATE` |
| `rollup`（00:20） | `task_failed job=rollup` | 重跑收敛（四张表全 upsert）；`cost_configured=0` 是价格规则缺失，补 `t_price_rule` 后重跑 |
| 任意任务 | `job_lock_held_by_other` | 另一副本持锁；租约 90s、每 20s 续约、剩余 < 30s 可接管。持续 5 分钟不接管说明持锁进程未退出，先确认再重启 |
