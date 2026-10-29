# Go / No-Go 判定

判定基准：T23 §5.6 的八条门槛，**全部满足才可 Go**。本页只引用已落盘证据；「未验证」一律记 `❌`，不折算为通过。

## 一、八条门槛逐条判定

| # | 门槛 | 判定 | 依据 |
| :--- | :--- | :--- | :--- |
| 1 | 82 项中 `❌ == 0`，`⚠️ ≤ 8` 且均已登记 | ❌ | `matrix-82.md`：`✅66 / ⚠️14 / ❌2`。`❌` 为 F4-3（一键导入后端未实现）、F14-6（回落无日志计数），门槛要求 0；`⚠️` 14 > 8。14 条 `⚠️` 已在 `deviations.md` D-01–D-04、D-07、D-10、D-13–D-20 登记，但「已登记」不能抵消数量超限 |
| 2 | G-01–G-12 全 `✅` | ❌ | `matrix-gwt.md`：`✅5 / ⚠️7`。`⚠️` 为 G-02（`est=7920` vs 7,200）、G-03（`PAID` vs `SUCCESS`）、G-05（单行版本化 vs 独立旧版行）、G-06（分组端点无地板）、G-07（`APPLIED` vs `PENDING`）、G-09（无 24h 延迟）、G-12（`/pricing` DOM 未验）。12/12 用例 PASS 不等于 12/12 断言与 PRD 一致 |
| 3 | 65 码断言通过且枚举行数一致 | ❌ | `matrix-codes.md`：PRD 65 行，`✅49 / ⚠️9 / ❌7`；`internal/apierr/codes.go` 常量 **76**。`❌` 为 `account_frozen`、`duplicate_request_in_flight`、`duplicate_request_id`、`upstream_key_unavailable`、`config_item_not_found`、`account_rpm_exceeded`、`content_policy_violation`。枚举行数与常量数不一致（65 vs 76），口径见 `errata.md` ERR-05 |
| 4 | 8 条对账断言在演练中 0 差异 | ❌ | 仓库内只有 `internal/recon` 的单测与新库回归记录，**无全量恢复后的对账演练报告**；T22 段 4（`scripts/backup.sh` / `restore.sh` / `docs/dr-plan.md`）未开始。`open-issues.md` OI-09 |
| 5 | 14 条 CI 门禁全绿 | ✅ | 交付总监 2026-10-29 复跑：`go test ./internal/acceptance/ -run TestCI_ -count=1 -v` → **14/14 PASS**（F04_2/F06_7/F07_1/F07_4/F11_3/F14_2/F14_4/F14_5/F14_6/F16_4/F16_6/F19_1/F19_4/F20_3），`ok 4.498s`。此前记录的「卡在 0022 表已存在」是本机共享库 `yunshu_test` 的一次漂移，复跑即过，**不构成门禁失败**；`OI-10` 降级为环境清理项（非阻塞） |
| 6 | 无 P0 级开放问题 | ❌ | `open-issues.md` 的阻塞项：OI-01（F4-3）、OI-02（F14-6）、OI-04（分组倍率可绕过毛利地板，资金正确性）、OI-05（提额通过无通知 + 邮件通道缺失） |
| 7 | 备份恢复演练通过 | ❌ | `scripts/backup.sh`、`scripts/restore.sh`、`docs/dr-plan.md` 均不存在；T22 §A7「全量恢复后 8 条对账断言 0 差异」无演练记录。`open-issues.md` OI-09 |
| 8 | 内部 SLO 未出现在任何对外页面 | ✅ | `grep -rnE "\bSLO\b\|99\.9\|P95\|RTO\|RPO" web/src/pages/site web/src/components/site web/src/content`（排除 `.test.`）无命中；公开接口 `internal/control/publicapi` 无 SLO / RTO / RPO 字段。唯一反向证据是 PRD §9.9–§9.11 明确「内部运维 SOP，不是对外承诺」 |

**通过数：2 / 8**（门槛 5 经交付总监复跑后由 `❌` 更正为 `✅`）。

## 二、结论

**No-Go。**

不放宽门槛的依据：

- 门槛 1 的两个条件同时不成立（`❌ 2 > 0`、`⚠️ 14 > 8`），不是「差一条」；
- 门槛 2 的 7 条 `⚠️` 中有 4 条是数值或状态口径冲突（G-02 / G-03 / G-07 / G-05），会导致按 PRD 对接的消费方读错状态或金额算式；
- 门槛 3 的 `❌` 中有 `content_policy_violation` 无生产者，F19-3 的申诉入口在生产无真实数据源；
- 门槛 4 / 7 是**无证据**（对账演练、备份恢复演练均未做），按 T23 §4「未验证 = `❌`」处理；
- 门槛 8 通过，只能说明没有把内部 SLO 写到对外页面，不能抵消其余六条。

## 三、要变 Go 必须做完的事（按优先级）

| 优先级 | 事项 | 关闭后达标 | 指向 |
| :--- | :--- | :--- | :--- |
| P0-1 | 实现 F4-3 的深链模板存储 / 运营配置面与 `import-credentials` 端点；在 P12 回落分支写结构化日志与计数并补断言 | 门槛 1 的 `❌ 2 → 0` | `open-issues.md` OI-01、OI-02；`matrix-82.md` F4-3、F14-6 |
| P0-2 | 分组写入路径补毛利地板、`internal` 分组二次确认、§6.4 套餐分支；修完后 G-06 才可转 `✅` | 资金正确性；门槛 2 减 1 条 `⚠️` | `open-issues.md` OI-04；`matrix-82.md` F16-5、`matrix-gwt.md` G-06 |
| P0-3 | 把 82 项 `⚠️` 从 14 降到 ≤ 8：至少完成 F4-2 导入端点、F10-3 `CODE_USED` + `REDEEM_BATCH`、F20-4 提额通过通知、F2-1 充值页读 `plan`、F1-1 文档改 400 + 向量模型用例、F16-1 / G-05 版本口径裁定 | 门槛 1 的 `⚠️ ≤ 8` | `deviations.md` D-01–D-14；`open-issues.md` OI-05 |
| P0-4 | 7 条 GWT `⚠️` 全部转 `✅`：G-02 公式回写、G-03 / G-07 状态名对齐、G-05 版本行、G-06 地板、G-09 24h 延迟、G-12 `/pricing` Playwright | 门槛 2 | `matrix-gwt.md` |
| P0-5 | 65 码收口：为 7 个 `❌` 逐码裁定（补生产者或按 PRD 单列「登记不实现」）；为 9 个 `⚠️` 补自动化触发用例；按 ERR-05 回写 PRD / TDS 与 T23 A4 | 门槛 3 | `open-issues.md` OI-03、OI-06、OI-07、OI-08 |
| P0-6 | 完成 T22 段 4：备份 / 恢复脚本、DR 计划、8 条对账断言恢复后校验；在预发环境各跑一次并回填报告 | 门槛 4、7 | `open-issues.md` OI-09 |
| P0-7 | 在干净库复跑 14 条 CI 并取得全绿记录；清理共享测试库 `yunshu_test` 的表 / `schema_migrations` 漂移 | 门槛 5 | `open-issues.md` OI-10 |
| P1-1 | 提额通过站内通知；邮件通道待供应商定后接 SMTP / API | 门槛 6 减 1 个 P0 项 | `open-issues.md` OI-05 |
| P1-2 | 上线前注入生产 `VITE_API_BASE_URL` | 发布配置 | `open-issues.md` OI-12 |
| P1-3 | G-13 空白带待用户提供 Chrome 版本 / 缩放 / 录屏；信息到位前不改源码 | 非门槛项 | `open-issues.md` OI-11 |

## 四、签署位

| 角色 | 结论 | 签署 | 日期 |
| :--- | :--- | :--- | :--- |
| 实现方（Claude Code） | No-Go | — | 2026-10-29 |
| 交付总监 | 待复核 | | |
| 需求方 | 待确认 | | |

> 复核人若要把结论改为 Go，必须先复跑本页八条门槛并给出可核对证据（命令 + 输出 + 文件:行），不得只引用本页结论。
