# 错误码台账（70 条）

> 本文件由 `apierr.MarkdownTable()` 生成，**不得手工编辑**。真相源是 PRD 附录 E.1 与 `internal/apierr/codes.go`。
> 重新生成：写一段 10 行的临时程序打印 `apierr.MarkdownTable()` 到本文件（临时程序不入库），或等 T15 的后台「错误码导出」接口。

## 与 PRD E.1 的关系

- HTTP 状态、`type`、可重试性、`hint` 规则、触发阶段 逐条对齐附录 E.1
- 码总数固定 **70**，`codes_test.go` 断言 `len(Specs()) == 70`
- 新增四码来自勘误 **E-21**（PRD E.1 尚未回写）：`session_expired`、`invalid_credentials`、`account_exists`、`invalid_query_parameter`；总数 65 → 69
- 新增一码来自 **T12 硬约束 2**（PRD E.1 无对应行）：`key_limit_reached`（409 `conflict_error`，不可重试）——免费档活跃 Key 已达 `quota.free_max_active_keys` 上限。此前该拒绝复用 `invalid_request_body`，把合法请求体报成「不是合法 JSON：count」，误导用户去改一个改了也没用的字段；总数 69 → 70

## 全量清单

| `code` | `type` | HTTP | `retry_after_seconds` | 可重试 | 归属 | 触发阶段 |
| --- | --- | --- | --- | --- | --- | --- |
| `invalid_api_key` | `authentication_error` | 401 | 0 | no | BUSINESS | P05 |
| `invalid_credentials` | `authentication_error` | 401 | 0 | no | BUSINESS | 控制台登录 |
| `session_expired` | `authentication_error` | 401 | 0 | no | BUSINESS | 控制台会话 |
| `key_expired` | `authentication_error` | 401 | 0 | no | BUSINESS | P05 |
| `key_disabled` | `authorization_error` | 403 | 0 | no | BUSINESS | P05 |
| `account_disabled` | `authorization_error` | 403 | 0 | no | BUSINESS | P05 |
| `account_frozen` | `authorization_error` | 403 | 0 | no | BUSINESS | P05 |
| `ip_not_allowed` | `authorization_error` | 403 | 0 | no | BUSINESS | P06 |
| `conflicting_auth_headers` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P05 |
| `model_not_found` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P07 |
| `model_not_available` | `upstream_error` | 503 | 30 | yes | BUSINESS | P07 |
| `model_not_in_group` | `authorization_error` | 403 | 0 | no | BUSINESS | P07 |
| `model_not_authorized` | `authorization_error` | 403 | 0 | no | BUSINESS | P07 |
| `invalid_request_body` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P04 |
| `invalid_query_parameter` | `invalid_request_error` | 400 | 0 | no | BUSINESS | 控制台接口 |
| `invalid_messages` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P04 |
| `invalid_temperature` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P04 |
| `max_tokens_exceeded` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P04 |
| `invalid_tools` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P04 |
| `invalid_image_count` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P04 |
| `invalid_image_size` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P04 |
| `invalid_image_url` | `invalid_request_error` | 400 | 0 | no | BUSINESS | P04 |
| `request_too_large` | `invalid_request_error` | 413 | 0 | no | BUSINESS | P02 |
| `request_read_timeout` | `invalid_request_error` | 408 | 5 | yes | BUSINESS | P02 |
| `unknown_endpoint` | `invalid_request_error` | 404 | 0 | no | BUSINESS | 兜底 |
| `insufficient_credits` | `billing_error` | 402 | 0 | no | BUSINESS | P09/P10 |
| `account_frozen_due_to_overdraft` | `billing_error` | 402 | 0 | no | BUSINESS | P09 |
| `price_not_configured` | `internal_error` | 500 | 0 | no | PLATFORM | P09 |
| `rpm_limit_exceeded` | `rate_limit_exceeded` | 429 | 60 | duration | BUSINESS | P11 |
| `tpm_limit_exceeded` | `rate_limit_exceeded` | 429 | 60 | duration | BUSINESS | P11 |
| `concurrency_limit_exceeded` | `rate_limit_exceeded` | 429 | 3 | duration | BUSINESS | P11 |
| `daily_quota_exceeded` | `rate_limit_exceeded` | 429 | 86400 | duration | BUSINESS | P11 |
| `rate_limiter_unavailable` | `service_unavailable_error` | 503 | 5 | yes | PLATFORM | P11 |
| `request_in_flight` | `conflict_error` | 409 | 1 | duration | BUSINESS | P08 |
| `idempotency_key_reused` | `conflict_error` | 409 | 0 | no | BUSINESS | P08 |
| `idempotent_replay_unsupported` | `conflict_error` | 409 | 0 | no | BUSINESS | P08 |
| `duplicate_request_in_flight` | `conflict_error` | 409 | 1 | duration | BUSINESS | P08 |
| `duplicate_request_id` | `conflict_error` | 409 | 0 | no | BUSINESS | L3 |
| `no_available_upstream` | `upstream_error` | 503 | 30 | yes | CONFIG | P12/P13 |
| `no_available_upstream_for_group` | `upstream_error` | 503 | 0 | no | CONFIG | P12 |
| `upstream_capacity_exceeded` | `upstream_error` | 503 | 30 | yes | UPSTREAM | P16 |
| `upstream_timeout` | `upstream_error` | 504 | 30 | yes | UPSTREAM | P15/P16 |
| `upstream_dns_failure` | `upstream_error` | 502 | 0 | no | UPSTREAM | P15 |
| `upstream_tls_failure` | `upstream_error` | 502 | 0 | no | UPSTREAM | P15 |
| `upstream_auth_failed` | `upstream_error` | 502 | 0 | no | PLATFORM | P16 |
| `upstream_model_not_found` | `upstream_error` | 502 | 0 | no | PLATFORM | P16 |
| `upstream_invalid_request` | `upstream_error` | 400 | 0 | no | BUSINESS | P16 |
| `upstream_invalid_response` | `upstream_error` | 502 | 10 | yes | UPSTREAM | P19 |
| `upstream_empty_response` | `upstream_error` | 502 | 0 | no | UPSTREAM | P19 |
| `upstream_key_unavailable` | `upstream_error` | 503 | 60 | yes | PLATFORM | P15 |
| `upstream_all_failed` | `upstream_error` | 502 | 30 | yes | UPSTREAM | E-09 |
| `client_aborted` | `client_error` | 499 | 0 | not_applicable | BUSINESS | 内部归类 |
| `convert_failed` | `internal_error` | 500 | 0 | no | PLATFORM | P14/P20 |
| `settlement_unavailable` | `service_unavailable_error` | 503 | 5 | yes | PLATFORM | P10 |
| `backpressure_stream_rejected` | `service_unavailable_error` | 503 | 5 | yes | PLATFORM | 背压 60% 档 |
| `backpressure_rejected` | `service_unavailable_error` | 503 | 2 | yes | PLATFORM | 背压 90% 档 |
| `config_unavailable` | `service_unavailable_error` | 503 | 10 | yes | PLATFORM | E-60 |
| `auth_unavailable` | `service_unavailable_error` | 503 | 5 | yes | PLATFORM | P05 |
| `internal_error` | `internal_error` | 500 | 0 | no | PLATFORM | 全局兜底 |
| `key_not_found` | `invalid_request_error` | 404 | 0 | no | BUSINESS | 控制台接口 |
| `key_limit_reached` | `conflict_error` | 409 | 0 | no | BUSINESS | 控制台接口 |
| `account_exists` | `conflict_error` | 409 | 0 | no | BUSINESS | 控制台注册 |
| `account_not_verified` | `authorization_error` | 403 | 0 | no | BUSINESS | 控制台接口 |
| `permission_denied` | `authorization_error` | 403 | 0 | no | BUSINESS | 运营后台 |
| `channel_not_found` | `invalid_request_error` | 404 | 0 | no | BUSINESS | 运营后台 |
| `channel_in_use` | `conflict_error` | 409 | 0 | no | BUSINESS | 运营后台 |
| `config_item_not_found` | `invalid_request_error` | 404 | 0 | no | BUSINESS | 运营后台 |
| `config_value_invalid` | `invalid_request_error` | 400 | 0 | no | BUSINESS | 运营后台 |
| `account_rpm_exceeded` | `rate_limit_exceeded` | 429 | 60 | duration | BUSINESS | P11（V1.0 不实现） |
| `content_policy_violation` | `invalid_request_error` | 403 | 0 | no | BUSINESS | P04 / §8.4 |

