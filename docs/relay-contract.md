# 协议映射契约（T08 第一段）

实现：`relaykit/ir`（唯一 IR）、`relaykit/openai`、`relaykit/anthropic`、`relaykit/bailian`。转换只有两段：`对外协议 → IR → 上游协议`，反向解码同样经 IR；三个 adapter 互不 import，也不 import `internal/`。

未识别的成员进入对应层级的 `Extra map[string]json.RawMessage`（请求顶层为 `Request.RawExtra`），编码时按原层级写回。建模字段优先于同名 Extra 键。上游拒绝未知字段时由其返回 400，网关映射为 `upstream_invalid_request`，本地不静默删除。

## 表 1：出站字段 → IR → 上游字段

| # | 客户端字段（OpenAI / Anthropic） | IR 字段 | OpenAI 上游 | Anthropic 上游 | 百炼上游 | Extra 归属 |
| :-- | :--- | :--- | :--- | :--- | :--- | :--- |
| 1 | `model` string | `Request.Model` | `model` | `model` | `model` | 无（建模） |
| 2 | `messages[].role` | `Message.Role` | `messages[].role` | `messages[].role`；`system` 提到顶层 | `messages[].role` | 无 |
| 3 | `messages[].content` string | `Content.Text` | `content` 字符串 | `content` 字符串 | `content` 字符串 | 无 |
| 4 | `content[].type="text"` | `ContentPart.Type` | 同 OpenAI | `content[].type="text"` | 同 OpenAI | 无 |
| 5 | `content[].text` | `ContentPart.Text` | `text` | `text` | `text` | 无 |
| 6 | `content[].type="image_url"` / `"image"` | `ContentPart.Type`、`ImageURL` | `image_url` | `image` | `image_url` | 无 |
| 7 | `image_url.url` / `source.url` | `ContentPart.ImageURL` | `image_url.url` | `source.type="url"` + `source.url` | `image_url.url` | 无 |
| 8 | `source.type="base64"` + `media_type` + `data` | `ImageURL`（data URL）、`MediaType` | 原 data URL 写入 `image_url.url` | 还原 `source.base64/media_type/data` | 原 data URL | 无 |
| 9 | `image_url.detail` | `ContentPart.Extra["detail"]` | `image_url.detail` | 无对应，作为块级未知成员写出 | 块级未知成员 | OpenAI `imageURL.Extra`；Anthropic/Bailian 上提到 `ContentPart.Extra` |
| 10 | 顶层 `system`（Anthropic）/ 首条 system 消息 | `Request.System` | 首条 `system` 消息 | 顶层 `system` | 首条 `system` 消息 | 无 |
| 11 | `stream` bool | `Request.Stream` | `stream` | `stream` | `stream` | 无 |
| 12 | `stream_options.include_usage` | `StreamOptions.IncludeUsage` | `stream_options.include_usage` | 不写；usage 随 `message_delta` 到达 | `stream_options.include_usage` | 无 |
| 13 | `stream_options` 其他成员 | `StreamOptions.Extra` | 同层写回 | 不写 | 同层写回 | `StreamOptions.Extra` |
| 14 | `max_tokens` int | `Request.MaxTokens *int64` | `max_tokens` | `max_tokens`；缺失返回 `ErrMaxTokensRequired` | `max_tokens` | 无 |
| 15 | `temperature` float | `Request.Temperature *float64` | `temperature` | `temperature` | `temperature` | 无 |
| 16 | `top_p` float | `Request.TopP *float64` | `top_p` | `top_p` | `top_p` | 无 |
| 17 | `stop` string／[]string；`stop_sequences` []string | `Request.Stop []string` | `stop` | `stop_sequences` | `stop` | 无 |
| 18 | `tools[].type="function"` | 不入 IR | 固定写 `"function"` | 无 `type` 成员 | 固定写 `"function"` | 无 |
| 19 | `tools[].function.name` / `tools[].name` | `Tool.Name` | `function.name` | `name` | `function.name` | 无 |
| 20 | `function.description` / `description` | `Tool.Description` | `function.description` | `description` | `function.description` | 无 |
| 21 | `function.parameters` / `input_schema` JSON | `Tool.Parameters json.RawMessage` | `function.parameters` | `input_schema` | `function.parameters` | 无（原样 JSON） |
| 22 | `tools[].function.strict` bool | `Tool.Extra["strict"]` | `function.strict` | tool 对象未知成员 `strict` | `function.strict` | `Tool.Extra` |
| 23 | `cache_control`（Anthropic tool / block / source） | `Tool.Extra` / `ContentPart.Extra` | 原样写到对应对象 | 原样写到 tool 或 block | 原样写到对应对象 | `Tool.Extra` / `ContentPart.Extra` |
| 24 | `tool_choice` `{"type":"function",...}` / `{"type":"tool","name":...}` | `Request.ToolChoice` raw | function 形状 | `{"type":"tool","name":"x"}` | function 形状 | 无（raw 保留） |
| 25 | `response_format` `{"type":"json_object"}` | `Request.ResponseFormat` string | `response_format.type` | 不写（表 2） | `response_format.type` | 无 |
| 26 | `n` int | `Request.N int` | `n` | 不写（表 2） | `n` | 无 |
| 27 | `messages[].name` | `Message.Name` | `name` | 不写（Anthropic 消息无该成员） | `name` | 无 |
| 28 | `messages[].tool_call_id` | `Message.ToolCallID` | `tool_call_id` | `tool_result.tool_use_id` | `tool_call_id` | 无 |
| 29 | 助手 `tool_calls[]` | `Message.ToolCalls` | `tool_calls[]`，`arguments` 是 JSON 字符串 | `content[]` `tool_use`，`input` 是对象 | `tool_calls[]` | 无 |
| 30 | `role="tool"` 消息 | `Message.Role`、`ToolCallID`、`Content.Text` | 原样 | 改写为 `role="user"` + `tool_result` 块 | 原样 | 无 |
| 31 | `content[]` `tool_use` 块 | `ContentPart.Type="tool_use"` + `ToolCalls` | 折叠进 `tool_calls` | 原样 | 折叠进 `tool_calls` | 块级未知成员入 `ContentPart.Extra` |
| 32 | `content[]` `tool_result` 块、`is_error` | `ContentPart.Type="tool_result"`、`ToolUseID`、`Extra["is_error"]` | 展开为独立 `role="tool"` 消息；`is_error` 无对应 | 原样 | 展开为独立 `role="tool"` 消息 | `ContentPart.Extra` |
| 33 | `logprobs` bool | `Request.RawExtra["logprobs"]` | 原样 | 原样（可能被 400） | 原样 | `Request.RawExtra` |
| 34 | `top_logprobs` int | `Request.RawExtra["top_logprobs"]` | 原样 | 原样（可能被 400） | 原样 | `Request.RawExtra` |
| 35 | `top_k` int | `Request.RawExtra["top_k"]` | 原样 | 原样 | 原样 | `Request.RawExtra` |
| 36 | `metadata` object | `Request.RawExtra["metadata"]` | 原样 | 原样；`metadata.user_id` 不建模 | 原样 | `Request.RawExtra`（三方均不建模） |
| 37 | `thinking` object | `Request.RawExtra["thinking"]` | 原样 | 原样 | 原样 | `Request.RawExtra` |
| 38 | `enable_search` bool | `Request.RawExtra["enable_search"]` | 原样 | 原样（可能被 400） | 原样 | `Request.RawExtra` |
| 39 | `search_options` object | `Request.RawExtra["search_options"]` | 原样 | 原样 | 原样 | `Request.RawExtra` |
| 40 | `seed`、`user` 及其他未识别顶层键 | `Request.RawExtra` | 同层写回 | 同层写回 | 同层写回 | `Request.RawExtra` |
| 41 | message／content part／tool 级未识别键 | `Message.Extra`、`ContentPart.Extra`、`Tool.Extra` | 同层写回 | 同层写回 | 同层写回 | 对应层级 Extra |
| 42 | `/v1/embeddings` `input` string／[]string | `Request.EmbedInputs []string` | 单值写成字符串，多值写成数组 | 不适用 | `input` | 无 |
| 43 | `/v1/embeddings` `encoding_format` | `Request.RawExtra["encoding_format"]` | 原样 | 不适用 | 原样 | `Request.RawExtra` |
| 44 | `/v1/images/generations` `prompt` | `Request.Prompt` | `prompt` | 不适用 | `prompt` | 无 |
| 45 | `/v1/images/generations` `size` | `Request.Size` | `size` | 不适用 | `size` | 无 |
| 46 | `/v1/images/generations` `n` | `Request.N` | `n` | 不适用 | `n` | 无 |
| 47 | `/v1/images/generations` `response_format` | `Request.ResponseFormat` | `response_format` | 不适用 | `response_format` | 无 |
| 48 | 响应 `id` | `Response.ID` | `id` | `id` | `id` | 无 |
| 49 | 响应 `object` / `type` | `Response.Object`（编码器按目标协议重写） | `object="chat.completion"` | `type="message"` | `object="chat.completion"` | 无 |
| 50 | 响应 `created` | `Response.Created int64` | `created` | 不写 | `created` | 无 |
| 51 | 响应 `model` | `Response.Model` | `model` | `model` | `model` | 无 |
| 52 | 响应消息 `role` | `Choice.Message.Role` | `message.role` | `role` | `message.role` | 无 |
| 53 | 响应文本 | `Message.Content.Text`／`Parts` | `message.content` | `content[]` text block | `message.content` | 无 |
| 54 | 响应工具调用 | `Message.ToolCalls` | `message.tool_calls[]` | `content[]` `tool_use` | `message.tool_calls[]` | 无 |
| 55 | `finish_reason` / `stop_reason` | `Choice.FinishReason` | `stop`／`length`／`tool_calls` | `end_turn`／`max_tokens`／`tool_use` | 同 OpenAI | 无 |
| 56 | 响应 `stop_sequence` | `Response.StopSequence` | 不写 | `stop_sequence` | 不写 | 无 |
| 57 | `choices[].logprobs` | `Choice.Logprobs json.RawMessage` | `logprobs` | 原样写到响应块 | `logprobs` | IR 字段本身即 raw，不使用 Extra |
| 58 | 响应 `system_fingerprint` | `Response.Extra["system_fingerprint"]` | 同层写回 | 同层写回 | 同层写回 | `Response.Extra` |
| 59 | 响应 `container` | `Response.Extra["container"]` | 同层写回 | 同层写回 | 同层写回 | `Response.Extra` |
| 60 | 响应 `request_id` | `Response.Extra["request_id"]` | 同层写回 | 同层写回 | 同层写回 | `Response.Extra` |
| 61 | `refusal`、`citations`、`server_tool_use` 及其他响应未知键 | `Message.Extra`、`ContentPart.Extra`、`Usage.Extra` | 同层写回 | 同层写回 | 同层写回 | 对应层级 Extra |
| 62 | `_raw_response` | `Response.Extra["_raw_response"]` | 原文返回 | 原文返回 | 原文返回 | 内部哨兵，不序列化 |
| 63 | 流终止 `data: [DONE]` | `Chunk.Done` | `data: [DONE]` | `event: message_stop` | `data: [DONE]` | 无 |
| 64 | 首个角色帧 | `Chunk.Role`、`Delta.Role` | `choices[].delta.role` | `event: message_start` | `choices[].delta.role` | 无 |
| 65 | `content_block_start`（text） | `Chunk.Role` + 空 `Delta` | 不单独输出 | `event: content_block_start` | 不单独输出 | 无 |
| 66 | `content_block_start`（tool_use） | `Delta.ToolCalls[].ID/Name` | `delta.tool_calls[]` | `event: content_block_start` | `delta.tool_calls[]` | 块级 Extra 入 `Delta.Extra` |
| 67 | `content_block_delta` text_delta | `Chunk.HasContent=true`、`Delta.Content` | `delta.content` | `event: content_block_delta` `text_delta` | `delta.content` | 无 |
| 68 | `content_block_delta` input_json_delta | `Delta.ToolCalls[].Arguments` | `delta.tool_calls[].function.arguments` | `partial_json` | 同 OpenAI | 无 |
| 69 | `content_block_stop` | IR 无独立帧 | 不输出 | 由 `StreamEncoder.Finish` 发 | 不输出 | 无 |
| 70 | `message_delta` | `Chunk.Usage`、`Choice.FinishReason` | `usage` + `finish_reason` | `event: message_delta` | `usage` + `finish_reason` | 无 |
| 71 | `message_stop` | `Chunk.Done` | `data: [DONE]` | `event: message_stop` | `data: [DONE]` | 无 |
| 72 | 错误帧 | `Chunk.Error *IRError` | `data: {"error":{...}}`，无 `event:` 行 | `event: error` + `data` | `data: {"error":{...}}` | 上游原始错误对象保留在 `Chunk.Extra["error"]` |
| 73 | 仅 usage 帧 | `Chunk.Usage` | `choices:[]` + `usage` | 不单独成帧，随 `message_delta` | `choices:[]` + `usage` | 无 |
| 74 | `ping`／未知事件 | 空 `Chunk`（无内容、无 usage） | 不输出 | 丢弃 | 丢弃 | 事件级未知成员保留在 `Chunk.Extra` |
| 75 | keep-alive 注释行 | 不进入 IR | `: keep-alive` | `: keep-alive` | `: keep-alive` | 无 |

表 1 共 75 行，其中 23 行显式走 Extra／raw 或内部哨兵路径；未识别的成员一律按第 40、41 行规则透传。

## 表 2：不透传字段清单

| 字段 / 对象 | 处理 | 原因 |
| :--- | :--- | :--- |
| 客户端 `Authorization` / `x-api-key` | 不转发 | 上游凭据只来自渠道密文，发送前一刻解密；客户端 Key 不进入上游请求头 |
| 客户端 `anthropic-version` | 不转发；由 `anthropic.Headers` 写 `DefaultVersion="2023-06-01"` | 渠道协议版本由网关固定，避免客户端用旧版本改变上游解析 |
| 客户端 `Accept` / `Content-Type` | 网关重写 | 防止客户端请求头影响上游 SSE／JSON 解析 |
| `X-Request-Id`、`X-Credits-Preheld`、`X-Credits-Remaining` | 网关生成 | 响应头在首帧前定型；流式不再发 `X-Credits-Consumed` |
| `_raw_response`、`_event` | 不序列化 | 内部哨兵，仅用于 IR 往返与流事件解析 |
| `response_format` | 转 Anthropic 上游时不写 | Anthropic Messages 无该字段；本地不静默改写语义，P04 已按能力表拒绝不支持的组合 |
| `n` | 转 Anthropic 上游时不写 | Anthropic 单次只返回一个 message；Text 请求 `n!=1` 由 P04 拒绝 |
| `messages[].name` | 转 Anthropic 上游时不写 | Anthropic message 对象无 `name` 成员 |
| `stream_options` 及未知子键 | 转 Anthropic 上游时不写 | 无对应字段；usage 由 `message_start`／`message_delta` 返回 |
| 多个 `choices[1..]` | 转 Anthropic 响应时只取 `Choices[0]` | Anthropic message 只有一个内容数组；多候选仅 OpenAI 兼容上游产生 |
| usage-only 帧 | 转 Anthropic 客户端时不单独成帧 | Anthropic 的 usage 只能挂在 `message_delta` 上 |
| 无法解析的 SSE 帧 | OpenAI 客户端原样转发；Anthropic 客户端丢弃 | TDS §4.1 步骤 4、PRD E.4 规则 7 |
| 错误帧后的 `[DONE]` | 不补发 | 错误帧已是终止；补 `[DONE]` 会让客户端把部分结果当成功 |
| `usage.total_tokens` | 不用于计费 | 与拆分项不一致时以 `input_tokens`／`output_tokens` 为准（E-14） |
| 缺失的 `id` / `created` / `object` | 编码器写空值或协议常量 | 不伪造上游 ID；客户端不能把网关值当上游回执 |
| 上游响应体全文 | 不缓存、不落盘 | 非流式仅在途转发；`upstream_msg` 截断 512 字节并脱敏 |
| 渠道 Key 明文 | 只存在于 `dispatch.try` 局部变量 | 不进 `Ctx`、日志、attempt、错误体、metric 标签 |

## 表 3：token / usage 字段来源与语义

所有计数为 `int64`，不使用浮点。上游未返回 usage 时各字段为 0，由 P09 走本地估算；`TotalTokens` 从不参与计费口径。

| # | IR 字段 | OpenAI 来源 | Anthropic 来源 | 百炼来源 | 语义 |
| :-- | :--- | :--- | :--- | :--- | :--- |
| 1 | `Usage.InputTokens` | `usage.prompt_tokens` | `usage.input_tokens`（`message_start` 先给输入侧） | `usage.prompt_tokens`；缺失时读兼容字段 `usage.input_tokens` | 计入上游 prompt 的 token；缓存命中仍包含在该值内（Anthropic 除外，见第 3 行） |
| 2 | `Usage.OutputTokens` | `usage.completion_tokens` | `usage.output_tokens`（`message_delta` 为最终值） | `usage.completion_tokens`；缺失时读 `usage.output_tokens` | 生成 token；流式为累计值，不是增量 |
| 3 | `Usage.TotalTokens` | `usage.total_tokens` | `input_tokens + output_tokens`（协议无总数字段） | `usage.total_tokens`；缺失时 `prompt_tokens + completion_tokens` | 展示与日志用；计费只读第 1、2、4、5 行 |
| 4 | `Usage.CacheReadTokens` | `usage.prompt_tokens_details.cached_tokens` | `usage.cache_read_input_tokens` | `usage.prompt_tokens_details.cached_tokens` | 缓存命中读取的输入 token；与 `InputTokens` 的关系按各厂商口径，不做二次换算 |
| 5 | `Usage.CacheWriteTokens` | 无对应，保持 0 | `usage.cache_creation_input_tokens` | 无对应，保持 0 | 写入缓存的输入 token；仅 Anthropic 返回 |
| 6 | `Usage.CacheHitTokens` | 与 `CacheReadTokens` 同值 | 同左 | 同左 | V1.0 旧名，已废弃；`SetCacheTokens` 同时写两个字段，新代码只读 `CacheReadTokens` |
| 7 | `Usage.Extra` | `prompt_tokens_details.<key>` → `"prompt_tokens_details.<key>"`；`completion_tokens_details.<key>` 同理；usage 顶层未知键原样 | `usage` 顶层未知键原样（如 `server_tool_use`） | usage 顶层未知键、两级 details 的未知键 | 未知计数不丢弃；点号前缀表示嵌套层级，编码时还原到原对象 |
| 8 | `StreamOptions.IncludeUsage` | 来自请求 `stream_options.include_usage` | 不适用 | 来自请求 `stream_options.include_usage` | `NATIVE`／`STREAM_OPTIONS` 渠道强制注入 `true`；`UNKNOWN` 按 `STREAM_OPTIONS` 处理 |
| 9 | 流式 usage 到达时机 | 末尾 `choices:[]` 帧的 `usage` | `message_start.message.usage`（输入侧）+ `message_delta.usage`（输出侧） | 末尾 `choices:[]` 帧的 `usage` | 只有收到非零 `output_tokens` 才停止本地估算；连续 20 次缺失降级为 `ESTIMATE` |
| 10 | `Choice.FinishReason` | `finish_reason` | `stop_reason` | `finish_reason` | 映射：`stop`↔`end_turn`、`length`↔`max_tokens`、`tool_calls`↔`tool_use`；不影响 token 计数 |

表 1 共 75 行，表 2 共 17 行，表 3 共 10 行。
