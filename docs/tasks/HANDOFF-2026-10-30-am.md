# 交接单 · 2026-10-30（T40-b / T40-c / T41 / T42 收口后）

> 给下一个会话（新窗口）用。**第一件事就读这份**，再读 `docs/tasks/README.md`（派单入口）与 `docs/tasks/delivery/go-live.md`（验收结论）。
>
> 仓库：`D:/github_projects/yunshu-API`，分支 `main`，与 `origin/main` 同步，工作区干净。最新提交见 `git log -1`。

## 一、当前状态一句话

- **82 项功能点：✅75 / ⚠️7 / ❌0**（`acceptance/matrix-82.md`）
- **G-01–G-12：12/12 ✅**（`matrix-gwt.md`）
- **65 码：✅61 + ⬛4 + 🟨0**（`acceptance/matrix-codes.md`，本轮 T41/T42 把两个 `🟨` 清零）
- **八条 Go/No-Go 门槛：过 7 条**（`delivery/go-live.md`）
- **结论仍是 No-Go**，唯一阻塞是**门槛 6 的 OI-12**：生产构建未注入 `VITE_API_BASE_URL`——属发布配置，不是代码缺陷

## 二、本轮完成（4 张卡，全部 ✅）

| 卡 | 主题 | 提交 | 要点 |
| :--- | :--- | :--- | :--- |
| T40-b | `t_user_daily_quota` 生产者 | `92b2ed6` / `8fd38aa` | `daily_quota` job，CST 日切、幂等 upsert、补算 7 天、00:30 调度；R05 从空跑变有样本 |
| T40-c | `t_upstream_bill` 导入路径 | `8fdeffc` / `762e375` | `POST /api/admin/upstream-bills/import`，`MANUAL_UPLOAD`；`runExternal` 不再空跑 |
| T41 | 未带键 10 秒去重 | `2101014` / `57adbb2` / `fd072a2` | 合成键 `auto:`+sha256，scope 隔离 `gateway-auto`，非流式重放 200+头、流式 409 |
| T42 | 内容安全入口拦截 | `3254de3` / `7e1bbc9` | P06.5 关键词库 + 可配置 HTTP 适配器，fail-open，命中写 `t_content_policy_hit` + 403 |

四个码/功能的证据都已落在提交信息与台账里，每条都做过**反证**（改坏 → 测试红 → 还原 → 复绿）。

## 三、台账已回填的位置（别再按旧数字做判断）

| 文件 | 改动 |
| :--- | :--- |
| `delivery/go-live.md` | 门槛 3、4 转 `✅`；通过数 **5/8 → 7/8**；结论段重写；新增 **§四 上线 Blocking 清单（B-1…B-6）**；签署位填 2026-10-30 |
| `acceptance/matrix-codes.md` | `duplicate_request_in_flight`、`content_policy_violation` 两行 `🟨 → ✅` 并填触发用例名；统计改 `✅61 + ⬛4 + 🟨0` |
| `delivery/open-issues.md` | OI-03 / OI-06 / OI-07 / OI-14 / OI-15 标记关闭；OI-12 升级为「门槛 6 唯一阻塞 + 构建验证两步」；**全表只剩 1 个 `是`（OI-12）** |
| `BACKLOG.md` | 「上线前必办」由 1 行 G9 扩为 **RB-1…RB-6** 六条，RB-1/RB-2 为门槛 6 阻塞 |

## 四、⚠️ 本轮验证范围（**不要以为全仓绿了**）

本轮只对**改动所涉的包**做了验证，**全仓测试没有跑完**（会话中途被打断）。下一个会话若要宣称"全绿"，必须自己重跑。

已独立跑过且绿：

| 命令 | 结果 |
| :--- | :--- |
| `go test ./internal/jobs/` | `ok 12.227s`（含 T40-b 集成用例与 R05 断言） |
| `go test ./internal/control/ops/` | `ok 28.587s`（含 T40-c 四用例） |
| `go test -race ./internal/gw/ ./internal/idemstore/` | `ok 1.98s / 2.16s`（T41） |
| `go test -race ./internal/gw/ ./internal/control/ticket/` | `ok 2.91s / 4.92s`（T42 含申诉闭环） |
| `go test -race ./internal/config/` | `ok 23.495s`（T41 区间用例） |
| `bash scripts/check-eol.sh` | `EXIT=0` |

**未跑 / 未验证**：

- `go test ./...` 全仓（被打断，只跑到一半）
- `internal/acceptance` 的 14 条 `TestCI_`（门槛 5 的依据仍是 2026-10-29 的记录，本轮改了 `internal/gw/pipeline.go` 和 `internal/gw/deps.go`，**建议重跑一次确认**）
- `scripts/coverage-gate.sh`（覆盖率四阈值，未跑）
- `scripts/lint.sh`（容器内因 CRLF 无法执行 `set -o pipefail`，报 `invalid option name`；需在 Git Bash 或去 CR 后跑）
- `scripts/test-gwt.sh`、`scripts/smoke.sh`
- 前端 `vitest`（基线 48 文件 / 211 用例，本轮未跑）、`web` 的 Playwright e2e

> 交接单的立场：**没跑过的就是没验证，不得引用旧记录当本轮结果**。

## 五、环境事实（本轮新增/变更）

| 项 | 值 |
| :--- | :--- |
| **探演库** | `yunshu_drill` **已重建**：61 表、迁移 0026、`utf8mb4_0900_as_cs`。**内含 T40-b/T40-c 的 fixture，保留勿删**（`t_channel` 1 / `t_usage_record` 2 / `t_upstream_bill` 2 / `t_user_daily_quota` 1 / `t_reconcile_diff` 1 / `t_config_publish` 1） |
| 演练举证命令 | `MSYS_NO_PATHCONV=1 docker run ... -e ENV=local -e DB_DSN='.../yunshu_drill?...' golang:1.27 sh -c "go run ./cmd/recondrill -day 2026-10-25"` → `result=PASS total_diffs=0` |
| **Git Bash 路径转换** | `docker run -w /src` 会被 MSYS 改写成 Windows 路径并报 `working directory is invalid`。**必须加前缀 `MSYS_NO_PATHCONV=1`** |
| 测试库归属 | `internal/jobs` 的 TestMain 把库固定在 **`yunshu_jobs_test`**；`internal/gw` 用自己的 routing snapshot fixtures；`TEST_DB_DSN` 只借主机与账号 |
| `-race` | 必须 Debian `golang:1.27` + `CGO_ENABLED=1`；`-alpine` 无 gcc 会报 `-race requires cgo` |
| 开发栈 | `yunshu-mysql` / `yunshu-redis`（宿主 13306 / 16379）；`yunshu-stack-*` 全栈容器**未运行**，`scripts/recondrill.sh` 依赖的 `yunshu-stack_backend` 网络不存在，用它必失败 → 直接用上面的 `docker run` |
| 迁移 | 当前 **0026**；`docs` 与 `checksums.golden` 均一致 |
| 行尾 | 仓库带 `.gitattributes`（迁移 SQL / `checksums.golden` / `*.sh` / `*.lua` 钉 LF）；**`*.sh` 在 Windows 工作区仍是 CRLF**，容器内 `bash` 跑脚本会报 `set: pipefail: invalid option name` |
| 本机 Go | **没有**（GOROOT 空、PATH 无），后端一律用 `golang:1.27` 容器跑；因此任何「本机已过 -race」的说法都不成立，必须写清容器镜像 |
| e2e | `bash scripts/e2e-backend.sh` 起栈（重建 `yunshu_e2e` 库，57 表 + controld:8081），再 `cd web && E2E_BROWSER_CHANNEL=chrome npx playwright test`；若报 `docker ENOENT`，加 `E2E_DOCKER_BIN=<docker 绝对路径>` |

## 六、派单机制（本项目既定，别改）

- 实现方 = **Claude Code**，走 `claude -p` 无头派发（本机无 `claude_code_run` MCP）：

  ```bash
  claude -p "$(cat .workbuddy/dispatch/<卡>-prompt.md)" --model deepseek-flash \
    --permission-mode acceptEdits \
    --allowedTools "Read" "Write" "Edit" "Glob" "Grep" "Bash" \
    --output-format stream-json --verbose --max-turns 150 \
    < /dev/null > .workbuddy/dispatch/<卡>-run.jsonl 2>&1
  ```

  三个必带：`< /dev/null`（否则卡在等 stdin）、`--output-format stream-json`（否则中途无法判断死活）、`MSYS_NO_PATHCONV=1`（它内部的 docker 命令要用）。

- **一次只派一张卡**，复杂卡拆两段（实现 / 举证与反证），段间验收后再续。
- 实现方**不 commit**，交付总监复核后提交。
- 验收三件事：交付物核对 → 独立复跑 → **反证**（把实现改坏，用例必须红，然后还原并验证无残留）。
- 提交后 push；本轮 4 张卡共 9 个提交，已全部上远程。

## 七、已知工具坑

| 现象 | 处置 |
| :--- | :--- |
| 后台任务报 `failed` 但日志末尾 `EXIT=0` | 本机安全策略阻止 `reg.exe` 往 stderr 写 PROGRAM BLOCKED，导致退出状态误判。**以 `EXIT=` 为准** |
| `claude -p` 返回「退出码 null」 | 不等于没干活——实测已改过 5 个生产文件。**重试前先 `git status`** |
| 容器内 `bash scripts/*.sh` 报 `invalid option name` / `$'\r'` | 脚本在工作区是 CRLF。可在 Git Bash 直接跑，或容器内先去 `\r` |
| `docker run -w /src` 报 working directory invalid | 少了 `MSYS_NO_PATHCONV=1` |
| `git add` 输出大量 `LF will be replaced by CRLF` | 只是扫描告警；用 `git diff --numstat` 或 `git show --numstat HEAD` 确认真实改动 |
| 改坏代码做反证后忘记还原 | 每张卡都要求贴 `git diff --ignore-cr-at-eol` 证明无残留，还原后再跑一次全绿 |
| 单会话连开多张卡 | 曾累积到 **1.19M token** 触发 400、会话报废。**每张卡（最多每两段）换一个新会话** |
| 长 prompt 直接喂 CLI 易「退出码 null」 | 做法：**设计细节沉进任务卡面，派单只做路由**（1–2k token，指明读卡里哪几节）。卡面因此成为唯一真相源，实现方也不必靠长 prompt 猜 |
| 验收命令串太多 pipe | 统计口径写错会误判（曾用 `grep -c '^---'` 把 PASS 数成失败）。优先用 `go test` 的原生汇总行 |

## 八、下一步

代码侧已清零，剩下三件，按建议顺序：

1. **关闭 OI-12（唯一的门槛 6 阻塞）**：注入生产 `VITE_API_BASE_URL` → 用生产构建产物验证调试台「查看代码」的 base_url → 在 `go-live.md` §四 的 B-1/B-2 打勾。完成后八条齐备，可把结论改为 Go。
2. **补跑本轮未做的门禁**：`internal/acceptance` 的 14 条 `TestCI_`（本轮动过 gw 流水线）、`coverage-gate.sh`、前端 `vitest`、`e2e`。跑完把结果回填 `go-live.md` 门槛 5 与 `HANDOFF` 下一版。
3. **挂起项等外部条件**：OI-09（RPO/RTO 需 binlog 归档 + XtraBackup，运维立项）、OI-11（G-13 空白带等需求方给 Chrome 版本/缩放/录屏）、OI-13（套餐写入路径等产品决定范围）。

## 九、新会话开场建议

> 「读 `docs/tasks/HANDOFF-2026-10-30.md` 和 `docs/tasks/delivery/go-live.md`，先复跑本轮未跑的 CI 门禁，再处理 OI-12。」

如果需求方只想要「能不能上线」的答案：**代码侧已经可以，卡在发布配置 OI-12 那两步**。
