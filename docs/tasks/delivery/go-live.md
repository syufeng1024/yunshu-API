# Go / No-Go 判定

判定基准：T23 §5.6 的八条门槛，**全部满足才可 Go**。本页只引用已落盘证据；「未验证」一律记 `❌`，不折算为通过。

## 一、八条门槛逐条判定

| # | 门槛 | 判定 | 依据 |
| :--- | :--- | :--- | :--- |
| 1 | 82 项中 `❌ == 0`，`⚠️ ≤ 8` 且均已登记 | ✅ | `matrix-82.md`：`✅75 / ⚠️7 / ❌0`。T32（F14-6）、T33（F4-3）清掉两个 `❌`；T34 关闭 F2-1、F6-2、F8-1、F8-3、F20-4、F22-4、F22-6 七条 `⚠️`。剩余 7 条（F1-1、F2-2、F4-2、F10-3、F16-1、F16-3、F16-5）均已在 `deviations.md` 登记，且各有明确处置口径 |
| 2 | G-01–G-12 全 `✅` | ✅ | `matrix-gwt.md`：**12/12 ✅**。G-03/G-07 由 T35 按 ERR-03/ERR-04 勘误 PRD 后转 ✅；G-02/G-05/G-09 由 T37 按 ERR-09/ERR-10/ERR-11 裁定后转 ✅；G-12 由 T37 的 Playwright 用例（`web/e2e/pricing-price.spec.ts`）覆盖 `/pricing` DOM 后转 ✅（已做反证：改一位价格即红）。12/12 用例 PASS 且 12/12 断言与文档一致 |
| 3 | 65 码断言通过且枚举行数一致 | ❌ | **断言覆盖已达标，缺口只剩 2 个功能**：`matrix-codes.md` 现为 `✅59 + ⬛4 + 🟨2 = 65`、`⚠️ 0`（T39 段 2 补齐 9 个码的自动化用例：`key_disabled`、`account_frozen_due_to_overdraft`、`idempotent_replay_unsupported`、`upstream_tls_failure`、`upstream_auth_failed`、`upstream_model_not_found`、`client_aborted`、`convert_failed`、`auth_unavailable`，均经反证）；口径三方一致（65 + 11 == 76，OI-08 关闭）。**仍不能翻 ✅ 的原因**：`🟨` 两条是 PRD 要求 V1.0 实现但当前无生产者的功能——`content_policy_violation`（OI-03，需先定规则源）与 `duplicate_request_in_flight`（`idem.duplicate_window_seconds` 只声明未接线）。两者落地后本门槛即可关闭 |
| 4 | 8 条对账断言在演练中 0 差异 | ❌ | T40-a 后重跑（`dr-drill-2026-10-29.md` **§8**）：重建 `yunshu-app:local` → 只重启 `gatewayd` → 真实请求 `config_version=103`（当时 `cfg:ver=103`，`t_config_publish` 有该版本）→ 备份 + 恢复到 `yunshu_drill` → `recondrill --db yunshu_drill --day 2026-10-29` → `result=PASS total_diffs=0`，**退出码 0**，R01–R09 全 0。R09 已有**非种子样本**（演练库 `config_version>0` 共 9 行：8 种子 + 1 真实），反证把该行改成不存在的 `9999` 后 R09 检出 1 条 HIGH、`result=FAIL`，证明检查真生效。**仍不能翻 ✅**：§2 第 6 条 `t_upstream_bill`、第 7 条 `t_user_daily_quota` 在仓库内无生产写入方，对应检查 0 行空跑（OI-15） |
| 5 | 14 条 CI 门禁全绿 | ✅ | 交付总监 2026-10-29 复跑：`go test ./internal/acceptance/ -run TestCI_ -count=1 -v` → **14/14 PASS**（F04_2/F06_7/F07_1/F07_4/F11_3/F14_2/F14_4/F14_5/F14_6/F16_4/F16_6/F19_1/F19_4/F20_3），`ok 4.498s`。此前记录的「卡在 0022 表已存在」是本机共享库 `yunshu_test` 的一次漂移，复跑即过，**不构成门禁失败**；`OI-10` 降级为环境清理项（非阻塞） |
| 6 | 无 P0 级开放问题 | ❌ | 仍阻塞的项：OI-03 / OI-06 / OI-07 / OI-08（错误码表口径与 7 个无生产者码，门槛 3 同源）、OI-12（生产构建未注入 `VITE_API_BASE_URL`，G9 上线前必办）、OI-15（`t_user_daily_quota` / `t_upstream_bill` 无生产者，门槛 4 剩余唯一缺口）。**OI-16 已于 T40-a 关闭**（网关已写 `config_version`，真实请求 `=103` 并经反证）；OI-04（分组地板）、OI-05（提额通知）已于 T31 / T34 关闭 |
| 7 | 备份恢复演练通过 | ✅ | `dr-drill-2026-10-29.md`：`bash scripts/backup.sh` 退出 0（10.3s），manifest `schema_version=0026`、`sha256` 与 dump 一致（`3b261d6a…9358`）；连续两次恢复到 `yunshu_drill` 均退出 0（10.5s / 10.5s）；61 表、6 张关键表行数与 `yunshu` 逐表相等；`--db yunshu` 无 `--yes` 与 `--db yunshu_test` 均退出 1；口令未落 argv。残余：RPO ≤ 5 分钟未达标（无 binlog 归档），RTO 仅在 41 KB dump 上实测，见 `open-issues.md` OI-09 |
| 8 | 内部 SLO 未出现在任何对外页面 | ✅ | `grep -rnE "\bSLO\b\|99\.9\|P95\|RTO\|RPO" web/src/pages/site web/src/components/site web/src/content`（排除 `.test.`）无命中；公开接口 `internal/control/publicapi` 无 SLO / RTO / RPO 字段。唯一反向证据是 PRD §9.9–§9.11 明确「内部运维 SOP，不是对外承诺」 |

**通过数：5 / 8**（门槛 5 经交付总监复跑后由 `❌` 更正为 `✅`；门槛 1 于 T34 收口后转 `✅`；门槛 7 于 T36 段 2 演练后转 `✅`；门槛 4 于 T38 段 2 重跑后仍为 `❌`，机械 `total_diffs=0` 但 §2 第 6/7/8 条无有效样本）。

## 二、结论

**No-Go。**

不放宽门槛的依据：

- 门槛 1 已于 2026-10-29 达到（`❌ 0`、`⚠️ 7`）；
- 门槛 2 的 7 条 `⚠️` 中有 4 条是数值或状态口径冲突（G-02 / G-03 / G-07 / G-05），会导致按 PRD 对接的消费方读错状态或金额算式；
- 门槛 3 的 `❌` 中有 `content_policy_violation` 无生产者，F19-3 的申诉入口在生产无真实数据源；
- 门槛 4 仍是**不能举证**，但范围已收窄：T38 段 1 补齐 §2 映射、R03 改 `(created_at,id)` 排序；T40-a 让网关写入 `config_version`，真实请求已取证（`=103`）、R09 反证可检出（`dr-drill-2026-10-29.md` §8）。剩下的是 §2 第 6/7 条对应的 `t_upstream_bill` / `t_user_daily_quota` 无生产写入方（空跑，OI-15），按 T23 §4「未验证 = `❌`」处理；门槛 7 已由 T36 段 2 演练转 `✅`；
- 门槛 8 通过，只能说明没有把内部 SLO 写到对外页面，不能抵消其余六条。

## 三、要变 Go 必须做完的事（按优先级）

| 优先级 | 事项 | 关闭后达标 | 指向 |
| :--- | :--- | :--- | :--- |
| P0-1 | 实现 F4-3 的深链模板存储 / 运营配置面与 `import-credentials` 端点；在 P12 回落分支写结构化日志与计数并补断言 | 门槛 1 的 `❌ 2 → 0` | `open-issues.md` OI-01、OI-02；`matrix-82.md` F4-3、F14-6 |
| P0-2 | 分组写入路径补毛利地板、`internal` 分组二次确认、§6.4 套餐分支；修完后 G-06 才可转 `✅` | 资金正确性；门槛 2 减 1 条 `⚠️` | `open-issues.md` OI-04；`matrix-82.md` F16-5、`matrix-gwt.md` G-06 |
| P0-3 | 把 82 项 `⚠️` 从 14 降到 ≤ 8：至少完成 F4-2 导入端点、F10-3 `CODE_USED` + `REDEEM_BATCH`、F20-4 提额通过通知、F2-1 充值页读 `plan`、F1-1 文档改 400 + 向量模型用例、F16-1 / G-05 版本口径裁定 | 门槛 1 的 `⚠️ ≤ 8` | `deviations.md` D-01–D-14；`open-issues.md` OI-05 |
| P0-4 | 7 条 GWT `⚠️` 全部转 `✅`：G-02 公式回写、G-03 / G-07 状态名对齐、G-05 版本行、G-06 地板、G-09 24h 延迟、G-12 `/pricing` Playwright | 门槛 2 | `matrix-gwt.md` |
| P0-5 | 65 码收口：为 7 个 `❌` 逐码裁定（补生产者或按 PRD 单列「登记不实现」）；为 9 个 `⚠️` 补自动化触发用例；按 ERR-05 回写 PRD / TDS 与 T23 A4 | 门槛 3 | `open-issues.md` OI-03、OI-06、OI-07、OI-08 |
| P0-6 | 门槛 7 已由 T36 段 2 关闭；门槛 4 的映射与 R03 排序由 T38 段 1 修复；**第 8 条已由 T40-a（`f5d168d`）转为真实证据**——网关写 `cfg:ver`，真实请求 `config_version=103`、R09 反证可检出。剩余阻塞收窄为一个：`t_user_daily_quota`（R05）与 `t_upstream_bill`（`runExternal`）无生产写入方（OI-15） | 门槛 4 | `dr-drill-2026-10-29.md` §8、`open-issues.md` OI-15 |
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
