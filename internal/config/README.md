# internal/config

配置键元数据、不可变快照、版本一致性协议与 C2 策略注册表的唯一落点。

## 文件职责

| 文件 | 职责 |
| :--- | :--- |
| `key.go` | 45 个键的常量、类型、默认值、命名空间、运行时白名单、策略绑定（唯一来源） |
| `validate.go` | 按 int / bool / enum / decimal / json / string 校验；未知键与非法值返回 `config_value_invalid` |
| `snapshot.go` | `Snapshot`（不可变）+ 每命名空间一个 `atomic.Pointer`；版本一致性拉取；`NewStoreFromDB` 首次启动直读 |
| `watcher.go` | 订阅 `cfg:invalidate` + 30s 轮询 `cfg:ver` 兜底 |
| `publish.go` | 发布顺序：`t_config_item` → `t_config_publish` → `SET cfg:snap:*` → `INCR cfg:ver` → `PUBLISH` |
| `runtime.go` | `/admin/config` 的 12 键白名单与 V1 取值限制 |
| `strategies.go` | 8 个 C2 策略族的 V1.0 实现与反向注册入口 |
| `registry.go` | `Register` / `Resolve` / 启动期策略未注册门禁 |
| `redis.go` | 窄 Redis 接口与 go-redis 适配器，连接池由调用方注入 |
| `i18n/` | `i18n.DedicatedCopyPublic` 常量位 |

## 新增一个配置键的 4 步

1. **定义**：在 `key.go` 的 `keyTable` 增加一行。必须同时写全 `Key` 常量、`Type`、`Default`（JSON 字面量）、`Namespace`、`RuntimeWritable`、`Strategy`。同时把 `KeyCount` 从 45 改成 46。
   前置条件：TDS §3.7 已登记该键。T05 明文禁止未经 TDS 修改自行增加第 46 个键。
2. **校验**：为取值范围在元数据里补 `Int` / `Dec` / `Enum` / `Fields` / `MaxLen`。禁止在 `validate.go` 里为新键加 `if`；特例必须进元数据。默认值会在包 `init()` 里跑一次 `Parse`，非法默认值直接阻断启动。
3. **快照装载**：确认 `Namespace` 是 `ROUTING` / `RATELIMIT` / `PRICING` / `UPSTREAM` / `FEATURE` 之一。`UNASSIGNED` 只用于 `quota.overdraft_warn_days`，它不会进入任何快照。新增命名空间需要新增失效通道，V1.0 不允许。
4. **消费方与白名单判定**：消费方只引用 `Key*` 常量，不得出现键字符串字面量；用 `Store.Int/Bool/String/Decimal/Raw` 读取。只有同时满足「有运行时消费方」且「误改后果可即时发现」才可设 `RuntimeWritable: true`。白名单固定 12 键，变更需改 TDS §3.7，不是代码内决定。

第 4 步之后补两处测试：`TestKeyCount` 的 45/46 断言，以及 0018 种子的键集合比对（测试库 `yunshu_test`）。

## 版本一致性

`Loader` 的顺序固定为：

```
GET cfg:ver → GET cfg:snap:{namespace} → GET cfg:ver
```

两次版本号不一致，或快照内嵌 `version` 与读到的 `cfg:ver` 不一致，都丢弃本次结果重拉，最多 3 次。3 次仍失败则保留旧指针、记录 WARN、把错误返回给调用方。`Snapshot` 发布后不可修改，更新一律换新指针；读路径只有 `atomic.Pointer.Load`，没有锁。

发布顺序固定为「先写快照、后升版本」。反过来会出现「新版本号 + 旧快照」的窗口，节点无法仅靠双重读取发现。

订阅 `cfg:invalidate` 只是加速通道。`Watcher` 同时每 30s 读一次 `cfg:ver`；订阅失败、掉线、丢消息都不影响最终生效，最坏延迟 30s。

## 发布与运行时写入

- `Publisher.Publish`：运维发布路径，接受任意已分配命名空间的键；仍拒绝未注册的策略名，避免发布后启动门禁失败。
- `RuntimeWriter.Set`：`/admin/config` 路径，只接受 12 个白名单键。`quota.key_daily_mode` 只接受 `none`、`quota.reset_scope` 只接受 `account`、`quota.reset_tz` 只接受 `Asia/Shanghai`、`quota.rate_limit_order` 只接受 `concurrency>rpm>tpm>daily`；其余取值返回 400 `config_value_invalid`。
- `Publisher.PublishRaw`：只给无键命名空间（`ROUTING`）使用，路由/价格装载器自己组装 payload。同样受 256 KB 上限约束。

## 策略注册

业务包在自己的 `init()` 里调用 `config.Register(kind, name, impl)`；`internal/config` 不 import 任何业务包。进程启动时调用 `StartupCheck`，任何被配置引用的策略名没有注册就启动失败，错误里列出该 kind 的可用实现。

V1.0 内置实现在 `strategies.go` 注册：`deduct_policy/fifo_expiry`、`refund_policy/by_order`、`cost_source/manual`、`daily_meter/prehold`、`daily_meter/settle`、`key_daily_quota/none`、`reset_scope/account`、`tier_chain/model>channel`、`group_switch_mode/ops_only`。

## 测试环境

测试使用真实 MySQL `yunshu_test`（`TEST_DB_DSN`，默认 `127.0.0.1:13306`）与真实 Redis（`TEST_REDIS_ADDR`，默认 `127.0.0.1:16379`）。Redis 默认使用 **DB 15**（`TEST_REDIS_DB`），避免污染本地开发的 DB 0；`TestMain` 在开始和结束时 `FLUSHDB`，并把 `t_config_item` / `t_config_publish` 恢复到测试前的状态。
