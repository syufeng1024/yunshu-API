# T21 · 测试与 CI

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T21 |
| 阶段 | 6 · 质量与交付 |
| 优先级 | P0 |
| 依赖 | T09、T10、T12–T16 |
| 可并行 | 否（收口） |
| 交付根 | `**/*_test.go`、`e2e/`、`.github/workflows/`、`scripts/` |
| 状态 | ⬜ 未开始 |

## 1. 目标

让「文档说了什么」变成「机器每次提交都在验」。本卡不写业务逻辑，只做三件事：把 **14 条必进 CI 的验收条目**、**12 条 GWT**、**8 条对账断言**、**65 个错误码**变成流水线里的硬门禁，让后续任何改动都无法悄悄破坏已承诺的行为。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| PRD 附录 D.0 | 1320–1336 | 选点口径 + G-01–G-12 完整 GWT |
| PRD 附录 D.1 | 1572–1637 | 口径定档记录（数值的唯一来源） |
| PRD 附录 D.2 | 1637–1661 | 验收可执行性（定档后） |
| PRD 附录 D.3 | 1661–1750 | 82 项路径 + **14 条必进 CI 的清单** + 使用约定 |
| PRD 附录 E.5 | 2091–2104 | 附录 E 的验收要求 |
| TDS §8.6 | 4887–4942 | 迁移编号唯一性断言、checksum 断言 |
| TDS §6.2 | 3963–4137 | 65 码枚举（含「枚举表实际行数 == 声明数」的断言建议） |
| PRD §9 | 1012–1179 | 非功能指标（性能/可用性/降级） |

## 3. 14 条必进 CI 的验收项（清单固定，逐条一个用例）

| # | 编号 | 内容 | 类型 |
| :--- | :--- | :--- | :--- |
| 1 | F4-2 | 一键导入全链路无明文 Key（DOM/URL/localStorage/埋点） | 越权/泄露 |
| 2 | F7-1 | 用户 B 访问用户 A 的 Key → 403 或不存在 | 越权 |
| 3 | F7-4 | `PATCH` 携带 `group_code=internal` → 拒绝 | 越权 |
| 4 | F14-2 | 上游 Key 掩码展示 + 库中密文 | 安全 |
| 5 | F19-1 | 请求选择器只列 90 天内本人；越权提交被拒 | 越权 |
| 6 | F6-7 | 免费档 3 Key 上限 | 业务规则 |
| 7 | F11-3 | 正文 180 天 / 元数据 365 天清理 | 合规 |
| 8 | F14-4 | 渠道只允许 `contract` → `standard` 用户不被路由 | 路由 |
| 9 | F14-5 | 来源地「境外」拒绝保存 | 合规 |
| 10 | F14-6 | 专属渠道名单外用户候选集不含；停用后降级可见 | 路由 |
| 11 | F16-4 | 涨价公示期 < 7 天拒绝；降价即时 | 定价 |
| 12 | F16-6 | 同一模型两处配价冲突被拦截 | 定价 |
| 13 | F19-4 | 存在 `RESPONDED` 工单 → 注销被拒 | 合规 |
| 14 | F20-3 | 自审被系统级拒绝 | 资金 |

> 使用约定（PRD）：标「测试」的条目**必须进 CI**；其中越权类（F4-2 / F7-1 / F7-4 / F14-2 / F19-1）**新增接口时须同步补对应用例**。

## 4. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `scripts/test-gwt.sh` | G-01–G-12 十二条 GWT 的自动化执行入口（`go test -run TestGWT`） |
| 2 | `internal/acceptance/gwt_test.go` | 12 条 GWT 用例：每条用文档原文的 Given/When/Then 作为用例名与断言 |
| 3 | `internal/acceptance/ci14_test.go` | §3 的 14 条，一条一个 `TestCI_Fxx_x` |
| 4 | `e2e/security/` | Playwright：F4-2 一键导入不泄露、F7-1 越权、F19-1 选择器越权 |
| 5 | `internal/platform/migrate/guard_test.go`（强化） | 编号唯一 + `0007/0008` 未占用 + 每 up 有 down + 条数断言 |
| 6 | `internal/apierr/codes_test.go`（强化） | **枚举表实际行数 == 常量数 == 文档声明数（65）** |
| 7 | `internal/recon/assert_test.go` | 8 条对账断言各自的违例检测用例 |
| 8 | `internal/ledger/invariant_test.go` | 不变量：`SUM(ledger) == balance`、无孤儿流水、批次余额和一致 |
| 9 | `.github/workflows/ci.yml` | 阶段：lint → build → unit（`-race`，**必须在 Linux runner 上跑**：本机 Windows 无 gcc，`-race` 不可用）→ 集成（compose 起 MySQL/Redis）→ 迁移 up/down → e2e → 覆盖率门禁 |
| 10 | `.github/workflows/nightly.yml` | 夜间：全量 GWT + 8 断言 + 长跑稳定性（10 万请求 soak） |
| 11 | `scripts/coverage-gate.sh` | 覆盖率门禁：`internal/ledger` ≥ 85%、`internal/billing` ≥ 85%、`internal/apierr` ≥ 90%、整体 ≥ 70% |
| 12 | `docs/test-matrix.md` | 「验收条目 → 用例名 → 运行方式」三列表（审计与交接用） |
| 13 | `scripts/lint.sh` | `go vet` + `golangci-lint` + 硬约束扫描（浮点、`timestamp`、`context.Background`、`QuantumNous`、`encoding/json`） |
| 14 | `internal/load/soak_test.go` | 压测基线：200 QPS 持续 10 分钟，P99 采集（对照 PRD §9.4） |

## 5. 硬约束（违反即驳回）

1. **14 条一条不少**，命名可检索（`TestCI_F07_1` 形式），且**每条失败必须能定位到 F 编号**（测试名 + 失败信息）。
2. **12 条 GWT 用例名必须含 `G-0x`**，断言值用文档原文数值（如 G-02 的 6500/6000/7200）。
3. **CI 必须起真实 MySQL 8.0 + Redis**，不得用 mock 替代集成测试（单测可 mock，但 GWT 与 14 条必须跑真实依赖）。
4. **迁移 up/down 必须在 CI 里跑一遍**（含 `0013` 的 down 必须失败断言）。
5. **覆盖率门禁**：低于阈值即失败；不得用 `//nolint` 或空测试凑数。
6. **`-race` 必须开**：任何数据竞争即失败。本机 Windows 无 C 编译器，`-race` 只能在 Linux runner 或 `golang:1.24` 容器里跑（T04/T05/T06/T08/T09 的验收都是这么补的）。
7. **测试并发度固定 `-p 2`**：`go test ./...` 默认全并发时，多个测试二进制共用一个 Redis 测试库会互相 `FLUSHDB`，导致 `balance` / `breaker` 偶发失败（T09 实测约 1/3 轮次）。彻底解法是 per-package Redis DB（`redistest` 已留口子），本卡必须落地；在此之前流水线与本地一律 `-p 2`。
8. **覆盖率门禁需先对齐现状**：`internal/billing` 的实测覆盖是 **83.6%**，而下方门禁写的是 ≥85%。本卡开工时二选一：补测试到 85%，或把门禁调到实测值并登记理由——**不允许门禁一上线就红**。
7. **硬约束静态扫描**作为独立 CI job：浮点金额、`timestamp` 列、`context.Background()`、`QuantumNous`、`encoding/json` 越界、`http.Error` 越界。
8. **e2e 必须断言"无明文 Key"**：从 network、DOM、localStorage、sessionStorage、URL 五处抓取。
9. **失败用例不得 `t.Skip`**：未实现的功能可以 `t.Skip` 但必须带 `// TODO(Txx)` 卡号，且 CI 汇总输出 skip 清单。
10. **夜间任务失败必须可追溯**：保留日志与失败用例名。
11. **不得为了让 CI 绿而放宽断言**；如需放宽，先改文档再由交付总监确认。
12. **文档偏差必须显式断言**：错误码条数以枚举表实际行数为准（65），若与文档头部不一致则测试**失败并打印两处数字**（不得抹平）。

## 6. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `bash scripts/dev.sh ci` 本地一条命令跑完（build → vet → test -p 2 → lint）并全绿。**本机无 GNU make**，`make ci` 不是可用入口 | 复跑 |
| A2 | 14 条 CI 用例存在且通过（`grep -c "func TestCI_"` == 14） | 复跑 |
| A3 | 12 条 GWT 存在且通过（`grep -c "func TestGWT"` == 12） | 复跑 |
| A4 | 覆盖率达标（四条阈值） | 复跑 |
| A5 | 迁移 up/down 全绿，`0013.down` 断言失败 | 复跑 |
| A6 | 错误码断言：常量数 == 枚举表行数 == 65 | 复跑 |
| A7 | e2e 安全用例通过（F4-2 / F7-1 / F19-1） | 复跑 |
| A8 | 静态扫描 job：五类违规 0 命中 | 复跑 |
| A9 | 故意破坏（把 `raw_est` 改成 `est` 做准入）→ G-02 用例**必须失败** | 手工验证门禁有效 |
| A10 | 故意改 `0010` 注释 → checksum 用例失败 | 手工验证 |
| A11 | 夜间 soak：200 QPS × 10 min 无错误率突增，P99 有记录 | 手工 |
| A12 | `docs/test-matrix.md` 条目数与用例数一致（测试断言） | 单测 |

## 7. 自检命令

```bash
bash scripts/dev.sh ci
grep -c "func TestCI_" internal/acceptance/ci14_test.go     # 14
grep -c "func TestGWT" internal/acceptance/gwt_test.go      # 12
./scripts/coverage-gate.sh
```

## 8. 禁止事项

- 不得用单元测试冒充 GWT（GWT 必须走真实依赖）。
- 不得跳过或删除失败用例来"收尾"。
- 不得把越权用例标记为 `t.Skip`（它们是发布门禁）。
- 不得降低覆盖率阈值。

## 9. 完成回执

```
卡号: T21
CI 阶段: 逐阶段命令 + 耗时
14 条: 用例名清单 + 全绿输出
12 条 GWT: 用例名清单 + 关键断言值
覆盖率: 四条阈值实测值
门禁有效性: A9/A10 两个"故意破坏"验证输出
e2e: 安全用例输出
skip 清单: 有条目则列出并标卡号
遗留:
```
