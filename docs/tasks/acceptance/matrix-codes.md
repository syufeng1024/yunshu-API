# 65 个错误码验收对照表（matrix-codes）

| 项 | 值 |
| :--- | :--- |
| 数据来源 | `docs/_API 中转站 · 需求文档 V1.2.md` 附录 E.1（65 码）；HTTP 状态取自 `internal/apierr/codes.go` 的 `specs` 表实值 |
| 生成日期 | 2026-10-29 |
| 行数 | 65（PRD E.1 全集） |
| 实现常量数 | 76（`internal/apierr/codes.go`） |
| 差异 | PRD E.1 65 码 + 实现期新增 11 码 = 76；新增码见文末附录，不计入本表 65 行 |

触发用例名均先在仓库中 `grep` 确认存在；无自动化用例但存在生产者的码，给 `METHOD /path` 并记 `⚠️`。结论列三态：`✅` 有自动化触发用例；`⬛` 登记不实现（编号指向 `deviations.md`）；`🟨` 功能缺口待立项（本卡不写代码）。

| 码 | HTTP | 触发用例 | 结论 |
| :--- | :--- | :--- | :--- |
| invalid_api_key | 401 | `TestInvalidAPIKeyAndKeyExpiredAreDistinct` | ✅ |
| key_expired | 401 | `TestInvalidAPIKeyAndKeyExpiredAreDistinct` | ✅ |
| key_disabled | 403 | `TestKeyDisabledIs403AndDistinctFromInvalidAndExpired` | ✅ |
| account_disabled | 403 | `TestDisableReasonIsOperatorOnly` | ✅ |
| account_frozen | 403 | 无 | ⬛ 登记不实现（D-31） |
| ip_not_allowed | 403 | `TestInternalPipelineUsesInjectedKeyIDForP06` | ✅ |
| conflicting_auth_headers | 400 | `TestA6ConflictingAuthHeadersIs400` | ✅ |
| model_not_found | 400 | `TestA4A5ModelStageOrderAndCodes` | ✅ |
| model_not_available | 503 | `TestModelNotAvailableIs503WithNotice` | ✅ |
| model_not_in_group | 403 | `TestA4A5ModelStageOrderAndCodes` | ✅ |
| model_not_authorized | 403 | `TestA4A5ModelStageOrderAndCodes` | ✅ |
| invalid_request_body | 400 | `TestCI_F07_4` | ✅ |
| invalid_messages | 400 | `TestP04ChatValidation` | ✅ |
| invalid_temperature | 400 | `TestP04ChatValidation` | ✅ |
| max_tokens_exceeded | 400 | `TestP04ChatValidation` | ✅ |
| invalid_tools | 400 | `TestP04ChatValidation` | ✅ |
| invalid_image_count | 400 | `TestP04ImageValidation` | ✅ |
| invalid_image_size | 400 | `TestP04ImageValidation` | ✅ |
| invalid_image_url | 400 | `TestP04ChatValidation` | ✅ |
| request_too_large | 413 | `TestA12BodyOverTenMegabytesIs413` | ✅ |
| request_read_timeout | 408 | `TestA12BodyReadTimeoutIs408` | ✅ |
| unknown_endpoint | 404 | `TestP03UnknownPathAndWrongMethod` | ✅ |
| insufficient_credits | 402 | `TestGWT_G02_AdmissionUsesRawEstimate` | ✅ |
| account_frozen_due_to_overdraft | 402 | `TestLoginFrozenDueToOverdraftIs402` | ✅ |
| price_not_configured | 500 | `TestP09RejectsEndpointModelTypeMismatch` | ✅ |
| rpm_limit_exceeded | 429 | `TestGWT_G10_429Contract` | ✅ |
| tpm_limit_exceeded | 429 | `TestA2FourDimensionsIndividually` | ✅ |
| concurrency_limit_exceeded | 429 | `TestA2FourDimensionsIndividually` | ✅ |
| daily_quota_exceeded | 429 | `TestGWT_G11_DailyQuotaIsAccountScoped` | ✅ |
| rate_limiter_unavailable | 503 | `TestServerRateLimiterUnavailableIs503` | ✅ |
| request_in_flight | 409 | `TestA10ReplayWaitAndReuse` | ✅ |
| idempotency_key_reused | 409 | `TestMySQLKeyReusedForDifferentBody` | ✅ |
| idempotent_replay_unsupported | 409 | `TestIdempotentReplayWithoutStoredBodyIs409`（>64KB 截断见 `TestLargeUnaryResponseIsNotStoredForReplay`） | ✅ |
| duplicate_request_in_flight | 409 | 无 | 🟨 待 T41（功能未接线：idem.duplicate_window_seconds 无读取点） |
| duplicate_request_id | 409 | 无 | ⬛ 登记不实现（D-32） |
| no_available_upstream | 503 | `TestPipelineShortCircuitsAndNamesFailStage` | ✅ |
| no_available_upstream_for_group | 503 | `TestRouterGroupEmptyUsesDistinctCode` | ✅ |
| upstream_capacity_exceeded | 503 | `TestA3Upstream429Becomes503WithoutRetry` | ✅ |
| upstream_timeout | 504 | `TestA6BudgetBelowThresholdDoesNotRetry` | ✅ |
| upstream_dns_failure | 502 | `TestA5DNSFailureIsNotRetried` | ✅ |
| upstream_tls_failure | 502 | `TestUpstreamTLSFailureIs502WithoutRetry` | ✅ |
| upstream_auth_failed | 502 | `TestUpstreamAuthFailedMaps401And403To502` | ✅ |
| upstream_model_not_found | 502 | `TestUpstreamModelNotFoundMaps404To502` | ✅ |
| upstream_invalid_request | 400 | `TestUpstream400MapsTo400InvalidRequest` | ✅ |
| upstream_invalid_response | 502 | `TestMalformedBodyIs502WithoutRetry` | ✅ |
| upstream_empty_response | 502 | `TestEmptyChoicesIs502AndNotBilled` | ✅ |
| upstream_key_unavailable | 503 | `TestUpstreamKeyUndecryptableIs503UpstreamKeyUnavailable` | ✅ |
| upstream_all_failed | 502 | `TestPlaygroundGatewayUnreachableIs502` | ✅ |
| client_aborted | 499 | `TestClientAbortedMarksUsageRowAndSettle` | ✅ |
| convert_failed | 500 | `TestConvertFailedWhenBaseURLSchemeIsUnsupported` / `TestConvertFailedWhenResponseHasNoClientRepresentation` | ✅ |
| settlement_unavailable | 503 | `TestSettlementFailureAnswers503` | ✅ |
| backpressure_stream_rejected | 503 | `TestA9BackpressureThresholds` | ✅ |
| backpressure_rejected | 503 | `TestA6GatewayRejectsNewRequestOnRealBacklog` | ✅ |
| config_unavailable | 503 | `TestMissingChannelPayloadIs503ConfigUnavailable` | ✅ |
| auth_unavailable | 503 | `TestLoginAuthUnavailableWhenDependenciesFail` | ✅ |
| internal_error | 500 | `TestRecoverWritesInternalErrorWithoutStack` | ✅ |
| key_not_found | 404 | `TestForeignKeyIsNotFound` | ✅ |
| account_not_verified | 403 | `TestGWT_G07_InvoiceFlow` | ✅ |
| permission_denied | 403 | `TestPersonalAccountGets403OnEveryAdminRoute` | ✅ |
| channel_not_found | 404 | `TestAdminDetailUses404Codes` | ✅ |
| channel_in_use | 409 | `TestChannelDeleteChecksRoutesAndSoftDelete` | ✅ |
| config_item_not_found | 404 | 无 | ⬛ 登记不实现（D-33） |
| config_value_invalid | 400 | `TestConfigWhitelist` | ✅ |
| account_rpm_exceeded | 429 | 无（PRD E.1.10 登记但不实现） | ⬛ 登记不实现（PRD E.1.10 已裁定） |
| content_policy_violation | 403 | 无 | 🟨 待 T42 / OI-03（需先定规则源） |

结论统计：`✅ 59` + `⬛ 4` + `🟨 2` = **65 行**（`⚠️ 0`）。`✅` 含 T39 段 1 补上生产者的 `upstream_key_unavailable` 与段 2 补齐触发用例的 9 个码；`⬛` 为 `account_frozen` / `duplicate_request_id` / `config_item_not_found` / `account_rpm_exceeded`；`🟨` 为 `duplicate_request_in_flight` / `content_policy_violation`。

## 附录：实现期新增、不在 PRD E.1 的 11 码

以下 11 码存在于 `internal/apierr/codes.go`（合计 76 码），来源为 errata E-21 与 T11/T12/T15 的补充；不计入上表 65 行。

- `session_expired` — HTTP 401；控制台会话过期（E-21 / T11）。
- `invalid_credentials` — HTTP 401；登录凭证错误（E-21）。
- `account_exists` — HTTP 409；注册冲突（E-21）。
- `invalid_query_parameter` — HTTP 400；分页/排序参数非法（E-21）。
- `key_limit_reached` — HTTP 409；免费档活跃 Key 达上限（T12）。
- `login_locked` — HTTP 429；密码失败预算耗尽（T11）。
- `resource_not_found` — HTTP 404；运营后台目标行不存在（T15）。
- `resource_state_conflict` — HTTP 409；订单/退款/提额非法状态迁移（T15）。
- `trial_already_claimed` — HTTP 409；设备/手机号/IP 段/账号已领取体验（T15）。
- `invoice_already_issued` — HTTP 409；订单已开票（T15）。
- `price_conflict` — HTTP 409；同一模型存在多价源（T15）。

## 枚举行数断言

```text
$ grep -cE '^\| [a-z_]+' docs/tasks/acceptance/matrix-codes.md
65
$ grep -cE '^\s+Code[A-Za-z0-9]+\s+= "' internal/apierr/codes.go
76
$ grep -cE '^\s+Code: Code[A-Za-z0-9]+,' internal/apierr/codes.go
76
```

断言结论：PRD E.1 枚举行数 == 65；`apierr` 常量数 == 76；差值 11 已在附录逐条列明。T23 A4 已按 ERR-05 改写为「65 行（PRD E.1 基线）+ 附录 11 码（实现期新增）== `apierr` 76 常量 / 76 spec」。结论分布：`✅ 59 + ⬛ 4 + 🟨 2 = 65`（`⚠️ 0`）；`⬛` 三条对应 `deviations.md` D-31 / D-32 / D-33，`account_rpm_exceeded` 无偏差登记（PRD E.1.10 已裁定），`🟨` 两条分别待 T41 / T42。
