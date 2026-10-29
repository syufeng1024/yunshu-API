# 未解决项（open issues）

判定依据只有两条：**是否影响资金正确性**、**是否影响上线必选路径**（T23 §5.6 的八条门槛）。两条都不命中的，记「否」并保留跟踪；「未验证」不折算为通过。

| 编号 | 描述 | 影响 | 建议 | 责任方 | 是否阻塞上线 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| OI-01 | 一键导入后端（原为 F4-3 的 `❌`）：`/api/public/integration-links` 恒返空、无深链模板存储。**2026-10-29 已由 T33 关闭**：迁移 `0026` 建 `t_integration`，运营 `PATCH /api/admin/integrations/{toolKey}` 改模板，公开端点读库 | 无（已实现） | 余项：`POST /api/v1/integrations/import-credentials` 按 D-30 定稿 **V1.0 不实现**（平台不代收用户 Key），保持 404，需在 PRD 回写为「不实现」 | 产品（回写 PRD） | **否** |
| OI-02 | 专属渠道回落无读取与计数（原为 F14-6 的 `❌`）。**2026-10-29 已由 T32 关闭**：`fallback_allowed` 默认禁止回落，允许时落 warn + `gw_dedicated_fallback_total` | 无（已实现） | — | — | 否 |
| OI-03 | `content_policy_violation` 无生产者：`apierr` 有 spec（403），网关 P04/§8.4 的入口拦截钩子未接线；F19-3 的用例靠测试预置 `t_content_policy_hit` 通过。见 `matrix-codes.md` content_policy_violation | PRD E.1 承诺的内容安全拦截在运行时不会触发；F19-3 的申诉入口在生产无真实来源 | 按 PRD §10.1 在网关入口接内容安全钩子，命中写 `t_content_policy_hit` 并返回 403；补 `TestCI` 级用例 | 网关 + 风控后端 | **是**（上线必选路径：65 码断言不通过；F19-3 无真实数据源） |
| OI-04 | F16-5 / G-06 分组倍率保存不做毛利地板校验。**2026-10-29 已由 T31 关闭**：`UpdateGroup` 按分组范围逐模型校验（共享 `marginFloor`），`internal` 四项约束 + 缺成本价二次确认；G-06 补三条端点断言 | 无（已实现） | 余项：套餐分支无写入路径（OI-13） | — | 否 |
| OI-05 | F20-4 提额通过不写站内通知。**2026-10-29 已由 T34 关闭站内通知部分**：`ApproveQuotaApply` 生效后发 `QUOTA_AUDIT`（事务外、重复审批不发第二条） | 余下：邮件通道仍未实现（仓库无 SMTP 客户端，`Mailer` 为 nil 时 WARN） | 待供应商确定后接 SMTP/API；期间发票/退款只有站内通知 | 产品（定供应商） | **否**（站内通知已达 PRD 最低要求） |
| OI-06 | 6 个错误码无生产者（另 1 个 `content_policy_violation` 见 OI-03）：`account_frozen`、`duplicate_request_in_flight`、`duplicate_request_id`、`upstream_key_unavailable`、`config_item_not_found`、`account_rpm_exceeded`。见 `matrix-codes.md` | 65 码断言的 `❌` 共 7 个；其中 `account_rpm_exceeded` 已由 PRD E.1.10 裁定「登记但不实现」，其余 6 个是契约承诺但无触发路径 | 逐码裁定：能实现的补生产者（账号冻结、幂等冲突族、渠道 Key 不可用、配置项不存在）；`account_rpm_exceeded` 按「登记不实现」从 65 码通过断言中单列，不补假生产者 | 后端实现方 + 交付总监（逐码裁定） | **是**（上线必选路径：65 码断言不通过） |
| OI-07 | 9 个码有生产者但无自动化触发用例：`key_disabled`、`account_frozen_due_to_overdraft`、`idempotent_replay_unsupported`、`upstream_tls_failure`、`upstream_auth_failed`、`upstream_model_not_found`、`client_aborted`、`convert_failed`、`auth_unavailable`。见 `matrix-codes.md` | 这些码的 403/402/409/502/499/500/503 映射回归不可发现；`⚠️` 计数无法清零 | 用假上游（`internal/fakeupstream`）构造 TLS / 401 / 模型不存在 / 断连四类；用可控依赖故障构造 `auth_unavailable`；其余补表驱动用例 | 测试 + 后端 | **是**（验收门槛：65 码 `⚠️` 未清零） |
| OI-08 | 错误码口径未回写：PRD E.1 = 65，TDS §6.2 计数段 = 47，实现 = 76。见 `errata.md` ERR-05 | T23 A4「65 行 == `apierr` 常量数」在 76 码下无法成立；按任一方核对都会得到「不一致」 | 按 ERR-05 裁决回写：PRD E.1 保留 65 基线 + 11 码附录；TDS §6.2 的 47 计数段作废；T23 A4 改为「PRD 基线 65 + 已登记新增 11 == 76」 | 交付总监 + 文档 | **是**（验收门槛：枚举行数一致） |
| OI-09 | 备份恢复演练：**2026-10-29 已由 T36 段 2 关闭**。`scripts/backup.sh` / `restore.sh`、`docs/dr-plan.md`、`docs/tasks/delivery/dr-drill-2026-10-29.md` 齐备；备份退出 0、manifest `0026` / sha256 一致、两次恢复到 `yunshu_drill` 均退出 0、6 张关键表行数与源库一致，门槛 7 转 ✅。**残余**：RPO ≤ 5 分钟未达标（无 binlog 归档，RPO = 备份间隔，每日 1 次时最长 24 小时）；RTO 只在 41 KB dump 上实测 10.5 s，TDS §8.1.3 的 745 GB 稳态需 XtraBackup 8.0；无异地副本 | RPO/RTO 属 PRD §9.9 目标缺口，不是门槛 7 的演练判据；宿主机或磁盘损坏时库与备份同时丢失 | 单独立项：binlog 每分钟归档 + 异地/对象存储副本 + XtraBackup 物理备份；按 TDS §8.1.2 算式验收 RPO ≈ 63 s | 运维 | **否**（不阻断门槛 7；不得据此宣称 RPO/RTO 已达标） |
| OI-14 | 门槛 4 举证缺口：`docs/settlement.md` §2 的 8 条与 `internal/recon` R-01..R-08 不一一对应——§2 第 3/4/5/8 条无实现、第 1 条仅由 R03 部分覆盖，R02/R04/R06/R07/R08 在 §2 无独立行。演练实测 R03 在 `yunshu_drill` 报 1 条 CRITICAL（`subject=34`，`delta=-118,565,000`；id 顺序 ≠ `created_at` 顺序，资金合计经 R01 验证一致）。R05/R08/`runExternal` 因 `t_user_daily_quota` / `t_metric_model_daily` / `t_upstream_bill` 0 行空跑。见 `dr-drill-2026-10-29.md` §4–§5 | 门槛 4 维持 `❌`；不能宣称「8 条对账断言 0 差异」，也不能用其余 7 个 0 折算 | ① 交付总监裁定以 §2 还是 R-01..R-08 + `runExternal` 为举证口径；② 按裁定补实现或回写文档；③ 裁定 R03 排序（改排序属断言语义变更，需签字；禁止手工跳过 `subject=34`）；④ 让 R05/R08/`runExternal` 在演练快照中有数据；⑤ 重跑 `bash scripts/recondrill.sh --db yunshu_drill -day <业务日>` 至 `result=PASS total_diffs=0` | 交付总监 + 后端 | **是**（验收门槛 4） |
| OI-10 | CI 全绿证据：交付总监 2026-10-29 复跑 `go test ./internal/acceptance/ -run TestCI_ -count=1 -v` → **14/14 PASS**（`ok 4.498s`），门槛 5 已转 ✅；此前「0022 表已存在」为本机共享库 `yunshu_test` 漂移，复跑即过 | 仅环境层面；无功能影响 | 需要时重建 `yunshu_test`（`DROP/CREATE` + 重跑迁移）并保留输出；远端 CI 运行记录可补查 | 测试维护 | **否** |
| OI-11 | G-13：A 项布局空白带四次测量未复现（PageDown 后上方约 500px 全白；`/pricing` 标题与卡片间约 90px）。见 `BACKLOG.md` G13、T28 §9、T29 §8 | 无法定位根因，无法判断是否为环境 / 缩放 / 浏览器版本相关 | 等需求方提供 Chrome 版本、页面缩放、进入路径、录屏；信息到位前禁止加 padding 盲改 | 需求方（环境信息） | **否**（无证据、非资金；但不得当作已修复） |
| OI-12 | G9 生产构建未注入 `VITE_API_BASE_URL` 时，调试台「查看代码」的 base_url 回退到 `window.location.origin`。见 `BACKLOG.md` G9、``PlaygroundPage.tsx:174`` | 生成的示例代码域名错误，用户复制后调不通 | 上线前在 `deploy/.env.stack` / CI 构建参数注入生产 API 域名，并在上线检查清单打勾 | 运维 / 发布 | **是**（上线必选路径：发布配置项，非代码缺陷） |
| OI-13 | 套餐配置无写入路径：`plan_min_margin_bp`（10% 地板）与 `max_bonus_ratio_bp`（赠送 30% 护栏）只有配置键，`t_plan` 仅由迁移 `0009` 种子写入，仓库里既无 `/api/admin/plans` 端点，运营定价页也没有套餐 tab | F16-5 的套餐分支无法实现与验证；运营想调套餐只能直接改库（无校验、无审计） | 需产品决策范围：① 在 V1.0 内新增「套餐配置」管理面（端点 + 页面 + λ 四步校验），或 ② 明确「V1.0 套餐固定、随版本发布」，同时把 F16-5 的套餐分支标注为不适用 | 产品 + 后端 | **否**（不阻塞主交易路径，但 F16-5 无法转 ✅） |

## 与 Go/No-Go 门槛的对应

| 门槛 | 未解决项 |
| :--- | :--- |
| 82 项 `❌ == 0`、`⚠️ ≤ 8` | OI-01、OI-02；`⚠️` 14 条见 `deviations.md` |
| G-01–G-12 全 `✅` | `deviations.md` D-05、D-06、D-08、D-09、D-11、D-12、D-21 |
| 65 码断言通过且枚举行数一致 | OI-03、OI-06、OI-07、OI-08 |
| 8 条对账断言演练 0 差异 | OI-14 |
| 14 条 CI 门禁全绿 | OI-10 |
| 无 P0 级开放问题 | OI-01、OI-02、OI-04（资金）、OI-05 |
| 备份恢复演练通过 | 无（T36 段 2 已关闭；OI-09 仅保留 RPO/RTO 目标残余） |
| 内部 SLO 未出现在对外页面 | 无（本轮 grep 无命中，门槛通过） |
