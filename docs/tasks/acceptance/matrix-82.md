# 82 项功能点验收对照表（matrix-82）

| 项 | 值 |
| :--- | :--- |
| 数据来源 | `docs/_API 中转站 · 需求文档 V1.2.md` 附录 D.3「功能点验收路径（全量 82 项，2026-10-25 补）」 |
| 生成日期 | 2026-10-29 |
| 行数 | 82 |
| 结论口径 | 本文件由 T23 生成，结论口径见 T23 §4 |

本表把 PRD 附录 D.3 的 82 条功能点逐条落成可核验的对照行。前三列（F 编号、验收路径摘要、执行者档）照 PRD 原文录入：编号与表内顺序原样保留（含 F1-7/F1-6、F8-4/F8-3、F14-6/F14-5、F22-5/F22-6/F22-4 等表内乱序），路径摘要不合并、不改写口径，执行者档沿用 PRD「执行者」列原文。第 4–6 列由 T23 后续段落（实现落点、用例或证据、结论）填写；未填前统一为 `待填`，不得据此判断通过与否。

| F 编号 | 验收路径摘要 | 执行者档 | 实现落点（文件） | 用例或证据 | 结论 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| F1-1 | G：在售文本、图像模型各 1 个｜W：首页价格表切「图像」分组｜T：只显示图像模型；请求向量模型返回 404 `model_not_found` | 研发自验 | `web/src/components/site/PriceTable.tsx:72`、`web/src/lib/site.ts:37`、`internal/gw/p07_model.go:55` | `PriceTable.test.tsx > F1-1 / F1-6: the image tab shows only LISTED image models`（`npx vitest run` 50 passed）；`TestServerErrorPathsEndToEnd` 断言 delisted → 400 `model_not_found`（`go test ./internal/gw` ok）；无向量模型专项用例 | ⚠️ 偏差待登记：D.3 要求 404 `model_not_found`，实现与 PRD E.1/TDS 一致为 400，且向量模型分支无用例 |
| F1-2 | G：后台把模型 M 输入价改为 X｜W：刷新首页｜T：首页显示 X；前端 bundle 中搜不到该价格字面量 | 研发自验 | `web/src/components/site/PriceTable.tsx:48`、`web/src/components/site/PriceTable.tsx:53`、`internal/control/publicapi/models.go:60` | `PriceTable.test.tsx > F1-2: renders price values from props, not from a page literal`（改价 1234 后渲染 0.1234 积分/千 token；`npx vitest run` 50 passed） | ✅ |
| F1-3 | G：后台把标准版 RPM 改为 299｜W：打开定价页｜T：限流表显示 299 | 研发自验 | `web/src/components/site/PlanLimitTable.tsx:23`、`web/src/pages/site/PricingPage.tsx:66` | `PlanLimitTable.test.tsx > F1-3: renders a changed RPM value from the plan payload`（rpm=299 渲染 299 且无 300；`npx vitest run` 50 passed） | ✅ |
| F1-4 | G：接入点未接入健康检查｜W：打开首页接入点区块｜T：只展示线路与地址，不出现「可用」状态 | 需求方抽查 | `web/src/components/site/EndpointBlock.tsx:47`、`web/src/pages/site/LandingPage.tsx:316` | `EndpointBlock.test.tsx > F1-4: shows only lines and Base URLs when no health data exists`（断言无「可用/正常/在线」；`npx vitest run` 50 passed）；`LandingPage.test.tsx > A4/A5: does not mention availability in endpoints or contract/internal prices` 同断言 | ✅ |
| F1-5 | G：模型配了 contract 倍率 0.85｜W：打开首页价格表｜T：只显示 `standard` 单价，无合约价与内部价 | 研发自验 | `web/src/components/site/PriceTable.tsx:48`、`internal/control/publicapi/models.go:22` | `LandingPage.test.tsx > A4/A5: does not mention availability in endpoints or contract/internal prices`（断言无「合约价/内部价」；`npx vitest run` 50 passed）；`PublicModel` 仅含 `input_price_micro_per_k`/`output_price_micro_per_k`，未联表分组倍率 | ✅ |
| F1-7 | G：M1 推荐权重 100、M2 权重 0，且均已上架已配价｜W：访客打开首页｜T：M1 排在前且进「推荐模型」区块；两者权重都为 0 时该区块整体不展示，不空佔版位 | 研发自验 | `web/src/components/site/ModelRecommendBlock.tsx:24`、`web/src/components/site/ModelRecommendBlock.tsx:26`、`web/src/lib/site.ts:60` | `ModelRecommendBlock.test.tsx > F1-7: does not render at all when every weight is zero`；`> F1-7: orders cards by weight and then by publish time`（`npx vitest run` 50 passed） | ✅ |
| F1-6 | M1 已上架、M2 仅目录｜W：访问首页、模型广场、文档示例｜T：三处均无 M2；后台可查 | 需求方抽查 | `web/src/lib/site.ts:33`、`internal/control/publicapi/models.go:60`、`internal/control/model/catalog.go:130` | `PriceTable.test.tsx > F1-1 / F1-6`；`DocsPage.test.tsx > F1-6: never puts a CATALOG_ONLY model into the documentation examples`；`TestCatalogHidesCatalogOnlyAndCachePrice`（`go test ./internal/control/model` ok），三处均按 LISTED 过滤 | ✅ |
| F2-1 | G：未登录｜W：点「标准版」｜T：跳注册并带套餐标识；已登录则跳充值中心并预选该档 | 研发自验 | `web/src/lib/site.ts:106`、`web/src/pages/site/LoginPage.tsx:29`、`web/src/pages/console/RechargePage.tsx:151` | `PlanCards.test.tsx > F2-1: anonymous CTAs carry the plan marker into registration`；`> F2-1: authenticated CTAs go to recharge with the plan preselected`（仅断言 href=`/console/recharge?plan=PRO`，未验充值页行为；`npx vitest run` 50 passed） | ⚠️ 偏差待登记：`RechargePage` 未读取 `plan` 查询参数，登录态进入后仍预选第一个可购档 |
| F2-2 | G：打开定价页｜W：查看四档｜T：一张表含 RPM / TPM / 并发 / 日额度 / 积分 / 价格，无大段文字块 | 需求方抽查 | `web/src/components/site/PlanLimitTable.tsx:18`、`web/src/components/site/PlanCards.tsx:88` | `PlanLimitTable.test.tsx > F2-2: renders the four tiers in one table with exactly six columns`（表头仅 套餐/RPM/TPM/并发/日额度/价格；`npx vitest run` 50 passed）；赠送积分仅在 `PlanCards` 卡片展示 | ⚠️ 偏差待登记：对比表缺「积分」列，PRD 要求的积分字段未进同一张表 |
| F3-1 | G：打开任一章含代码块｜W：点复制｜T：剪贴板等于代码原文，出现「已复制」提示 | 研发自验 | `web/src/components/site/CodeBlock.tsx:14`、`web/src/components/CopyBlock.tsx:28`、`web/src/components/CopyBlock.tsx:34` | `CodeBlock.test.tsx > F3-1: copies the code byte-for-byte and shows the copied label`（`writeText` 收到原文、渲染「已复制」；`npx vitest run` 50 passed） | ✅ |
| F3-2 | G：打开文档站｜W：点左侧第 5 章｜T：正文切换但整页不重新加载 | 研发自验 | `web/src/pages/site/DocsPage.tsx:27`、`web/src/pages/site/DocsPage.tsx:31` | `DocsPage.test.tsx > F3-2: switches chapters on the client without a reload`（点击后 h2 切为「鉴权与请求头」；`npx vitest run` 50 passed） | ✅ |
| F3-3 | G：打开快速开始｜W：比对示例域名与 Key｜T：均为虚拟值，页首有「示例为虚拟值」声明 | 需求方抽查 | `web/src/pages/site/DocsPage.tsx:50`、`web/src/pages/site/DocsPage.tsx:52` | `DocsPage.test.tsx > F3-3: shows the virtual-value notice`（断言「示例为虚拟值」与「均为虚拟值」；`npx vitest run` 50 passed） | ✅ |
| F4-1 | G：打开 `/integrations`｜W：查看工具卡｜T：恰 5 张（Cursor / Claude Code / Cherry Studio / NextChat / OpenAI SDK） | 研发自验 | `web/src/content/toolIntegrations.ts:51`、`web/src/pages/site/IntegrationsPage.tsx:53` | `IntegrationsPage.test.tsx > F4-1: renders exactly five tool cards`；`toolIntegrations.test.ts > F4-1: contains exactly the five V1.0 tools`（`npx vitest run` 50 passed） | ✅ |
| F4-2 | G：已登录且持有 Key｜W：点一键导入，全程抓 DOM、URL、localStorage、埋点｜T：均无明文 Key；跳转后参数即丢弃 | 测试（安全项） | `web/src/lib/deepLink.ts:33`、`web/src/components/site/OneClickImportButton.tsx:39`、`internal/acceptance/ci14_test.go:37` | `OneClickImportButton.test.tsx > F4-2: never leaks the key into the DOM, attributes, title, page URL or analytics`；`TestCI_F04_2`（`go test ./internal/acceptance -run TestCI_F04_2` ok，断言 key 仅出现在创建响应）；`npx vitest run` 50 passed | ⚠️ 偏差待登记：`POST /api/v1/integrations/import-credentials` 未实现（用例断言 404），DOM/URL/localStorage 端到端留给 Playwright |
| F4-3 | G：后台修改某工具深链参数名｜W：刷新后点一键导入｜T：按新模板生成，前端未发版 | 研发自验 | `web/src/lib/deepLink.ts:8`、`web/src/components/site/OneClickImportButton.tsx:16`、`internal/control/publicapi/empty.go:53` | `deepLink.test.ts > F4-3: uses whatever parameter names the backend template declares`（`npx vitest run` 50 passed）；`TestEndpointsAndIntegrationLinksAreHonestlyEmpty` 证明 `/api/public/integration-links` 恒返空数组（`go test ./internal/control/publicapi` ok），后台无模板存储与配置面 | ❌ 无后端模板存储/运营配置，后台改参数名无法执行；仅前端按模板替换有单测 |
| F4-4 | G：未登录｜W：打开 `/integrations`｜T：一键导入置灰并提示先登录 | 研发自验 | `web/src/components/site/OneClickImportButton.tsx:52`、`web/src/pages/site/IntegrationsPage.tsx:53` | `OneClickImportButton.test.tsx > F4-4: is disabled for anonymous visitors and points to login`；`IntegrationsPage.test.tsx > F4-4: a supported tool is disabled with a login hint for anonymous visitors`（`npx vitest run` 50 passed） | ✅ |
| F4-5 | G：修改文档第 9 章某客户端说明｜W：看对应工具卡｜T：卡片内容同步变化，不存在第二份文案 | 研发自验 | `web/src/content/toolIntegrations.ts:51`、`web/src/pages/site/docs/DocsChapters.tsx:417`、`web/src/pages/site/IntegrationsPage.tsx:53` | `DocsPage.test.tsx > F4-5: chapter 9 renders the shared five-tool content source`；`IntegrationsPage.test.tsx > F4-5: renders the generic OpenAI SDK fallback`（两处同读 `TOOL_INTEGRATIONS`；`npx vitest run` 50 passed） | ✅ |
| F5-1 | 见 G-08（提额申请的完整 GWT） | — | `web/src/components/console/RaiseQuotaDialog.tsx:29`、`web/src/pages/console/OverviewPage.tsx:217`、`internal/control/fund/quota_apply.go:93` | `TestGWT_G08_QuotaApplyApproval`（`internal/acceptance/gwt_test.go:832`：提交→`PENDING`、审批后次日 CST 00:00 生效 30 天、驳回不改额度；`go test ./internal/acceptance -run TestGWT_G08_QuotaApplyApproval` PASS）；`TestA6QuotaApplyApproveAndEffective`（`internal/control/fund/quota_apply_test.go:14`） | ✅ |
| F5-2 | G：该账户当日 10 次调用｜W：刷新概览｜T：24h 曲线与调用数一致；断网时图表报错而非显示假数据 | 研发自验 | `internal/control/user/overview.go:120`、`web/src/pages/console/OverviewPage.tsx:40`、`web/src/pages/console/OverviewPage.tsx:196` | `TestOverviewAggregates`（`internal/control/user/overview_live_test.go:83`：`hourly` 恰 24 个整点且 `requests` 求和等于当日调用数）；`OverviewPage.test.tsx > F5-2: a failed request renders an error state, not zero values`（断网渲染 `role=alert`，断言不出现 `¥0.00`/`0%`） | ✅ |
| F5-3 | G：普通账户｜W：打开概览｜T：无来源渠道标识（V1.0 不实现该逻辑） | 研发自验 | `internal/control/user/overview.go:120`、`web/src/pages/console/OverviewPage.tsx:126` | `TestOverviewAggregates`（`internal/control/user/overview_live_test.go:83`：断言响应 JSON 无 `source_channel` 键）；`OverviewPage.test.tsx > F5-3: never renders a source-channel badge in V1.0`（断言无「来源渠道」） | ✅ |
| F6-1 | G：5 个 Key，其中 2 个已停用｜W：搜名称片段 + 筛「已停用」｜T：返回两者交集 | 研发自验 | `web/src/pages/console/KeysPage.tsx:43`、`internal/control/key/list.go:58` | `TestListSearchIntersectsStatus`（`internal/control/key/key_test.go:297`：`q` 与 `status` 同时拼入 `WHERE … AND …`）；`KeysPage.test.tsx > A3 / F6-1: sends name search and status filter together so the API returns the intersection`（`q=prod` 与 `status=DISABLED` 同一请求） | ✅ |
| F6-2 | G：点新建密钥｜W：填名称 → 选模型范围 → 设有效期 → 设 IP 白名单 → 生成｜T：四步可前进后退，**无「选分组」步骤**（§5.3 只读） | 研发自验 | `web/src/components/console/CreateKeyDialog.tsx:27`、`web/src/components/console/CreateKeyDialog.tsx:138`、`internal/control/key/create.go:72` | `TestCreateRequiredFields`（`internal/control/key/key_test.go:230`：四步必填项缺失时各自报字段名）；`STEPS` 仅名称/模型范围/有效期/IP 白名单四项，`group_code` 在创建体中被拒（`TestGroupCodeIsRejectedAndReadOnly`，`key_test.go:184`）；`KeysPage.test.tsx > F6-5: the wizard rejects a batch count outside the 1–10 backend range`（前进至第 4 步）；`goBack`（`CreateKeyDialog.tsx:138`）无自动化断言 | ⚠️ 偏差待登记：「上一步」回退分支无自动化用例，仅前进路径被覆盖 |
| F6-3 | G：创建成功｜W：关闭弹窗后重进详情｜T：只见前 8 位 + 掩码，完整 Key 不可再查 | 需求方抽查 | `web/src/components/console/KeySecretOnce.tsx:34`、`internal/control/key/detail.go:79` | `TestCreatePlaintextOnceAndDetailMasked`（`internal/control/key/key_test.go:27`：仅创建响应含明文，详情/列表恒为掩码）；`KeysPage.test.tsx > A5 / F6-3: the plaintext is shown once, and only the masked prefix remains after closing`；`KeySecretOnce.test.tsx > F6-3: shows the full plaintext and drops it when the dialog closes` | ✅ |
| F6-4 | G：点停用｜W：弹二次确认后取消｜T：状态不变；确认才生效 | 研发自验 | `web/src/pages/console/KeysPage.tsx:304`、`internal/control/key/update.go:42` | `KeysPage.test.tsx > A6 / F6-4: cancelling the disable confirmation leaves the status unchanged`（取消后 `api.patch` 未调用）；`TestDisableRequiresConfirm`（`internal/control/key/key_test.go:336`：无 `confirm=true` 返回 400，重复停用为幂等 no-op） | ✅ |
| F6-5 | G：数量填 10｜W：批量创建｜T：生成 10 个同配置 Key，结果页可一次复制全部 | 研发自验 | `web/src/components/console/KeySecretOnce.tsx:58`、`internal/control/key/create.go:205` | `TestBatchCreateTen`（`internal/control/key/key_test.go:260`：响应 10 个互异明文、库中 10 行）；`KeySecretOnce.test.tsx > F6-5: a batch of 10 can be copied in one click`（`复制全部` 写入换行拼接的 10 个 Key）；`KeysPage.test.tsx > F6-5` 校验批量数越界被拦 | ✅ |
| F6-6 | G：`standard` 用户｜W：进密钥详情｜T：分组只读，无升档/降档入口 | 需求方抽查 | `web/src/pages/console/KeyDetailPage.tsx:328`、`internal/control/key/detail.go:79` | `KeyDetailPage.test.tsx > F6-6 / F7-4: the group is read-only and has no upgrade, downgrade or switch control`（分组区块标「只读」，无升档/降档/切换按钮）；`TestGroupCodeIsRejectedAndReadOnly`（`internal/control/key/key_test.go:184`） | ✅ |
| F6-7 | G：免费档已有 3 个活跃 Key｜W：再创建｜T：拒绝并提示升级；停用 1 个后可再建 | 测试 | `web/src/pages/console/KeysPage.tsx:53`、`internal/control/key/create.go:61` | `TestCI_F06_7`（`internal/acceptance/ci14_test.go:267`：第 4 个返回 409 `key_limit_reached` 且消息含上限「3」，停用 1 个后 201；已运行 PASS）；`TestFreeTierThreeActiveKeys`（`internal/control/key/key_test.go:73`）；`KeysPage.test.tsx > A9 / F6-7: greys out the create button when the free tier is at its active-key ceiling` | ✅ |
| F7-1 | G：用户 A 的 Key｜W：用户 B 访问 `/console/keys/{A的id}`｜T：403 或「密钥不存在」，不泄露存在性 | 测试（安全项） | `internal/control/key/detail.go:79`、`web/src/pages/console/KeyDetailPage.tsx:246` | `TestCI_F07_1`（`internal/acceptance/ci14_test.go:102`：B 对 A 的 Key 发 GET/PATCH/DELETE 均 404 `key_not_found` 且响应不含明文，库内名称/分组未变；已运行 PASS）；`TestForeignKeyIsNotFound`（`internal/control/key/key_test.go:150`）；`KeyDetailPage.test.tsx > F7-1: a key that is not owned renders the server error without leaking existence` | ✅ |
| F7-2 | G：打开密钥详情｜W：静置 30s｜T：RPM / TPM / 并发在 5~10s 周期内刷新 | 研发自验 | `web/src/pages/console/KeyDetailPage.tsx:104`、`web/src/hooks/useRealtimeMetric.ts:37`、`internal/control/key/detail.go:79` | `useRealtimeMetric.test.tsx > F7-2: polls on the configured interval and surfaces the newest value`（按配置间隔轮询并取最新值，失败置 `null`）；默认间隔 8000ms（`useRealtimeMetric.ts:37`），页面文案「每 8 秒刷新」由 `KeyDetailPage.test.tsx > F7-2: polls realtime limits and shows the masked key only` 断言；`TestDetailRealtimeMetrics`（`internal/control/key/key_test.go:486`） | ✅ |
| F7-3 | G：修改模型权限｜W：二次确认后保存｜T：生效且操作日志含前后值 | 研发自验 | `web/src/pages/console/KeyDetailPage.tsx:154`、`internal/control/key/audit.go:68`、`internal/control/key/update.go:42` | `KeyDetailPage.test.tsx > F7-3: editing model permissions requires confirmation and the audit log keeps before/after`（确认前不发 PATCH，确认后 PATCH `model_ids:[11,12]`）；`TestModelScopeChangeAudit`（`internal/control/key/key_test.go:390`：审计行 `before/after` 与变更前后模型 ID 一致） | ✅ |
| F7-4 | G：构造 PATCH 携带 `group_code=internal`｜W：提交｜T：400 / 403 拒绝，分组保持不变 | 测试（安全项） | `internal/control/key/update.go:58`、`internal/control/key/create.go:72`、`web/src/pages/console/KeyDetailPage.tsx:328` | `TestCI_F07_4`（`internal/acceptance/ci14_test.go:140`：PATCH 与创建携带 `group_code=internal` 均 400 `invalid_request_body` 且消息点名 `group_code`，库内仍为 `standard`；已运行 PASS）；`TestGroupCodeIsRejectedAndReadOnly`（`internal/control/key/key_test.go:184`）；`KeyDetailPage.test.tsx > F6-6 / F7-4` 断言 UI 无分组切换入口 | ✅ |
| F8-1 | G：后台改模型上下文长度｜W：刷新模型广场｜T：显示新值 | 研发自验 | `internal/control/model/catalog.go:226`、`web/src/pages/console/ModelsPage.tsx:137` | `TestCatalogUsesStoredContextLength`（`internal/control/model/catalog_test.go:198`：库内改为 123456 后 `/api/console/models` 返回 123456）；模型广场「上下文」列无前端断言 | ⚠️ 偏差待登记：仅后端取数有用例，`ModelsPage.tsx:137` 的上下文列渲染无自动化断言 |
| F8-2 | G：某模型输入 1500 微积分/千 token｜W：查看广场单价｜T：显示 0.15 积分 + 人民币参考值（1 元 = 100 积分） | 研发自验 | `internal/control/model/catalog.go:407`、`web/src/pages/console/ModelsPage.tsx:67` | `TestCatalogPriceConversion`（`internal/control/model/catalog_test.go:169`：1500 微积分/千 token → `0.15` 积分、`0.0015` 元/千 token，且不乘分组倍率）；`ModelsPage.test.tsx > F8-2: renders micro-credit prices through the shared formatter and never a cache price`（渲染「输入 0.15 积分/千 token」与人民币参考行） | ✅ |
| F8-4 | G：M1 权重高于 M2｜W：在模型广场按类型筛选「文本」｜T：筛选结果仍按推荐权重降序，未退回录入序 | 研发自验 | `internal/control/model/catalog.go:167`、`web/src/pages/console/ModelsPage.tsx:78` | `TestCatalogOrderingSurvivesFilter`（`internal/control/model/catalog_test.go:97`：`ORDER BY recommend_weight DESC, published_at DESC`，加 `model_type=TEXT` 后 M1 仍在 M2 前）；`ModelsPage.test.tsx > F8-1 / F8-4: type and protocol filters go to the API and the response order is preserved`（前端不重排，保留响应顺序） | ✅ |
| F8-3 | G：M2 为仅目录，且缓存计价关闭｜W：查看广场｜T：无 M2；在售模型标注流式 / 工具调用，**不显示缓存价** | 需求方抽查 | `internal/control/model/catalog.go:130`、`web/src/pages/console/ModelsPage.tsx:160` | `TestCatalogHidesCatalogOnlyAndCachePrice`（`internal/control/model/catalog_test.go:123`：`CATALOG_ONLY` 不出现，缓存关闭时 wire 层不含 `cache_*` 字段）；`ModelsPage.test.tsx > F8-3: an empty backend catalog renders the empty state instead of placeholder models`、`> F8-2: … never a cache price`；流式/工具调用徽标无断言 | ⚠️ 偏差待登记：流式/工具调用徽标渲染（`ModelsPage.tsx:160`）无自动化断言 |
| F9-1 | G：选近 7 日 + 模型 X｜W：切到「按渠道」｜T：筛选条件保持，数据按渠道重算 | 研发自验 | 待填 | 待填 | 待填 |
| F9-2 | G：某月消费 12,345 积分｜W：看月度账单｜T：显示 123.45 元 | 研发自验 | 待填 | 待填 | 待填 |
| F9-3 | G：设置筛选后导出｜W：检查 CSV｜T：仅含筛选内数据，列与页面一致 | 研发自验 | 待填 | 待填 | 待填 |
| F10-1 | 见 G-03（支付回调幂等与权益到账） | — | 待填 | 待填 | 待填 |
| F10-2 | 见 G-03（支付成功前积分不增加） | — | 待填 | 待填 | 待填 |
| F10-3 | G：使用已兑换过的兑换码｜W：提交｜T：拒绝并说明原因；审核中心生成的批次可正常兑换 | 研发自验 | 待填 | 待填 | 待填 |
| F10-4 | G：存在 3 种状态订单｜W：筛「已支付」+ 搜订单号｜T：返回交集 | 研发自验 | 待填 | 待填 | 待填 |
| F10-5 | 见 G-07（开票，含未实名分支） | — | 待填 | 待填 | 待填 |
| F11-1 | G：有成功与失败日志｜W：按状态 + 模型筛选｜T：返回交集 | 研发自验 | 待填 | 待填 | 待填 |
| F11-2 | G：一次请求重试 1 次｜W：查看日志详情｜T：时间线显示 2 次尝试，各自耗时与结果 | 研发自验 | 待填 | 待填 | 待填 |
| F11-3 | G：造 366 天前元数据 + 181 天前正文｜W：跑清理任务｜T：元数据在、正文已删（正文 180 / 元数据 365） | 测试 | 待填 | 待填 | 待填 |
| F12-1 | G：余额 100 积分｜W：发 1 次请求｜T：余额减少，账本新增 1 条 CONSUME | 研发自验 | 待填 | 待填 | 待填 |
| F12-2 | G：打开调试台｜W：查看快捷示例 prompt｜T：有示例，点示例不自动计费 | 研发自验 | 待填 | 待填 | 待填 |
| F12-3 | G：余额 0｜W：点发送｜T：拦截并提示充值，`t_upstream_attempt` 无新增行 | 研发自验 | 待填 | 待填 | 待填 |
| F14-1 | G：删除渠道后访问其详情 URL｜W：进入页面｜T：提示「渠道不存在或已被移除」，无 500 | 研发自验 | 待填 | 待填 | 待填 |
| F14-2 | G：新建渠道填上游 Key｜W：查看列表与数据库｜T：界面掩码展示，库中为密文 | 测试（安全项） | 待填 | 待填 | 待填 |
| F14-3 | G：权重 100 改为 10｜W：观察后续路由｜T：新权重生效，操作日志含前后值 | 研发自验 | 待填 | 待填 | 待填 |
| F14-4 | G：渠道仅允许 `contract`｜W：`standard` 用户请求｜T：不被路由到该渠道 | 测试 | 待填 | 待填 | 待填 |
| F14-6 | G：渠道 C1 填专属用户 U1｜W：U1 与 U2 各发一次同名模型请求｜T：U1 命中 C1、U2 候选集不含 C1；C1 停用后 U1 走 §3.4.1 降级且日志可见，**未见静默回落** | 测试 | 待填 | 待填 | 待填 |
| F14-5 | G：渠道表单来源地选「境外」｜W：保存｜T：**拒绝保存并提示**；境内可正常保存 | 测试 | 待填 | 待填 | 待填 |
| F16-1 | 见 G-05（调价不回溯历史账单） | — | 待填 | 待填 | 待填 |
| F16-2 | G：输入负数或 0 单价｜W：保存｜T：拒绝并指出具体字段 | 研发自验 | 待填 | 待填 | 待填 |
| F16-3 | G：改某模型输出价｜W：刷新官网价格表与控制台广场｜T：两处都显示新价 | 研发自验 | 待填 | 待填 | 待填 |
| F16-4 | G：涨价且生效时间距现在 3 天｜W：保存｜T：拒绝（公示期 < 7 天）；降价允许即时生效 | 测试 | 待填 | 待填 | 待填 |
| F16-5 | 见 G-06（分组倍率与毛利地板）；套餐分支另有四条约束见 §6.4 | — | 待填 | 待填 | 待填 |
| F16-6 | G：同一模型在两处配了不同单价｜W：保存｜T：只保留唯一真相源，冲突被拦截并指出两份来源 | 测试 | 待填 | 待填 | 待填 |
| F16-7 | G：`已上架` → `仅目录`｜W：保存｜T：不要求公示期，写操作日志并生成公告草稿 | 研发自验 | 待填 | 待填 | 待填 |
| F17-1 | G：模型 `仅目录` → `已上架`｜W：保存｜T：自动生成一条待发布的「上新」公告草稿 | 研发自验 | 待填 | 待填 | 待填 |
| F17-2 | G：公告生效时间设为明日 00:00｜W：保存后等待到点｜T：到点自动发布；「活动」类不得与调价公示冲突 | 研发自验 | 待填 | 待填 | 待填 |
| F17-3 | G：上架状态已变更但公告草稿未发布｜W：观察模型状态｜T：上架仍生效，公告不反向回滚状态 | 研发自验 | 待填 | 待填 | 待填 |
| F19-1 | G：用户 A 有 100 天前的请求，用户 B 有一个 request_id｜W：A 打开选择器；A 构造 B 的 request_id 提交｜T：选择器只列 90 天内本人请求；越权提交被拒 | 测试（安全项） | 待填 | 待填 | 待填 |
| F19-2 | G：有 `OPEN` 与 `RESPONDED` 各 1 条｜W：按状态筛选并打开详情｜T：筛选正确；详情显示「已响应 / 剩余 xx」 | 研发自验 | 待填 | 待填 | 待填 |
| F19-3 | G：请求被 403 `content_policy_violation` 拦截｜W：发起内容申诉工单｜T：自动带出命中规则 ID 与 `request_id` | 研发自验 | 待填 | 待填 | 待填 |
| F19-4 | G：存在 `RESPONDED` 工单｜W：提交注销｜T：拒绝并列出工单号 | 测试 | 待填 | 待填 | 待填 |
| F20-1 | G：五类单据各有数据｜W：打开审核中心｜T：默认落在 `PENDING` Tab，列表按提交时间**升序**（先到先审） | 研发自验 | 待填 | 待填 | 待填 |
| F20-2 | G：点驳回但不填理由｜W：提交｜T：被拒；填理由后用户侧对应入口可见该理由 | 研发自验 | 待填 | 待填 | 待填 |
| F20-3 | G：同一账号既是操作人又是审批人｜W：点审批｜T：**系统级拒绝**（按钮不可用或报错），不是靠流程约定 | 测试（资金项） | 待填 | 待填 | 待填 |
| F20-4 | G：通过一条提额申请｜W：查看通知｜T：站内通知到达；发票 `ISSUED`、退款 `REFUNDED` / `REFUND_FAILED` 额外发邮件 | 研发自验 | 待填 | 待填 | 待填 |
| F21-1 | G：运营回复工单｜W：用户查收｜T：状态自动置 `RESPONDED`，用户收到邮件 | 研发自验 | 待填 | 待填 | 待填 |
| F21-2 | G：工单超过 4h 未响应｜W：观察工单与会话状态｜T：仍在 `OPEN`、进入 §6.1 告警区，**不自动关闭** | 研发自验 | 待填 | 待填 | 待填 |
| F21-3 | G：回复 + 改状态 + 写内部备注｜W：查操作日志｜T：三条记录含操作人、时间、前后状态、是否有内部备注 | 研发自验 | 待填 | 待填 | 待填 |
| F22-1 | G：在模型主数据改名｜W：查看定价表｜T：定价表同步显示新名，无需二次维护 | 研发自验 | 待填 | 待填 | 待填 |
| F22-2 | G：别名 `x` 已属 M1｜W：给 M2 也配 `x` 并保存｜T：拒绝并列出冲突模型 | 研发自验 | 待填 | 待填 | 待填 |
| F22-3 | G：`已上架` → `仅目录`｜W：保存｜T：生成待发布公告草稿，记录操作人与理由 | 研发自验 | 待填 | 待填 | 待填 |
| F22-5 | G：M1 为 `仅目录`｜W：给 M1 设推荐权重 5 并保存｜T：拒绝保存并提示；改回 `已上架` 后可设；`已上架` 模型被改为 `仅目录` 时权重自动失效 | 研发自验 | 待填 | 待填 | 待填 |
| F22-6 | G：已上架且已配价的 M2｜W：推荐权重 0 改为 10｜T：保存成功，操作日志含前后值与操作人；首页推荐区块内该模型位置前移 | 研发自验 | 待填 | 待填 | 待填 |
| F22-4 | G：存在 `仅目录` 模型｜W：查看列表并筛选｜T：可按来源地 / 类型 / 上架状态筛选；`仅目录` 行单独标色 | 研发自验 | 待填 | 待填 | 待填 |
