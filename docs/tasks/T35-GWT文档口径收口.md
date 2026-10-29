# T35 · GWT 文档口径收口（G-03 / G-07）

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T35 |
| 阶段 | 6 · 质量与交付（收口） |
| 优先级 | P1（Go/No-Go 门槛 2 需要 G-01–G-12 全 ✅，当前 6/12） |
| 依赖 | T23（矩阵）、T13（状态枚举） |
| 交付根 | `internal/acceptance/gwt_test.go`、`docs/_API 中转站 · 需求文档 V1.2.md`、`docs/tasks/acceptance/matrix-gwt.md` |
| 状态 | ✅ 通过（2026-10-29；GWT 6/12 → 8/12） |
| 派单方式 | 单段 ≤ 10 分钟 |

## 1. 问题

G-03 与 G-07 的**断言本身是对的**（`PAID` / `APPLIED`，与 DDL 一致），但测试里留着一行「PRD 写的是 SUCCESS / PENDING」的 DEVIATION 日志，矩阵因此把这两条判为 ⚠️。`errata.md` 的 ERR-03 / ERR-04 已经裁决「以 DDL 为准」，缺的只是把**文档事实错误改掉**、并移除那两行已经不成立的日志。

按 T23 §5 硬约束 8，文档回写只允许做两件事：补迁移编号、按 `errata.md` 修**事实错误**。本卡正是后者。

## 2. 要做的事

| # | 文件 | 内容 |
| :--- | :--- | :--- |
| 1 | `docs/_API 中转站 · 需求文档 V1.2.md` | 附录 D 的 G-03 里订单成功状态 `SUCCESS` 改为 `PAID`；G-07 里发票初始状态 `PENDING` 改为 `APPLIED`。**只改这两处事实**，不改设计、不改措辞、不顺手优化；改动处行尾加 `（勘误 ERR-03 / ERR-04，2026-10-29）` 标记 |
| 2 | `internal/acceptance/gwt_test.go` | 删掉 `gwt_test.go:410` 与 `:830` 两行 `t.Logf("G-0x DEVIATION: ...")` 及其上方那条「PRD writes X / schema uses Y」的注释；**断言一行不动**（`state != "PAID"`、`!= "APPLIED"` 保留），把注释改成「PRD 已按 ERR-03/ERR-04 勘误为 PAID / APPLIED」 |
| 3 | `docs/tasks/acceptance/matrix-gwt.md` | G-03、G-07 两行改为 ✅，实测值写「断言 `PAID` / `APPLIED` 与 DDL 一致；PRD 已勘误」，并去掉对已删日志的引用 |
| 4 | `docs/tasks/delivery/deviations.md` | D-08（G-03）、D-09（G-07）标为已关闭，指向本次勘误 |

## 3. 硬约束

1. **不许改断言**：两条断言本来就是对的方向，删日志不等于放宽。
2. 不许改 PRD 其它任何内容（尤其不许动结论、数值、章节结构）。
3. 不动 `/v1/*`、不动代码逻辑、不加测试依赖。
4. `bash scripts/check-eol.sh` 退出码 0；不 commit / 不 push。

## 4. 自检

```bash
cd /d/codes/yunshu-api
export MSYS_NO_PATHCONV=1
docker run --rm -v "D:/codes/yunshu-api:/src" -v yunshu-gomod:/go/pkg/mod -w /src \
  -e CGO_ENABLED=1 \
  -e TEST_DB_DSN='root:root@tcp(host.docker.internal:13306)/yunshu_test?parseTime=true&loc=UTC&charset=utf8mb4' \
  -e TEST_REDIS_ADDR='host.docker.internal:16379' \
  golang:1.27 sh -c "go test ./internal/acceptance/ -run 'TestGWT_G03|TestGWT_G07' -count=1 -v 2>&1 | grep -E '^(--- |.*DEVIATION|ok|FAIL)'"
grep -c "DEVIATION" internal/acceptance/gwt_test.go   # 期望：只剩 G-02 / G-05 / G-09（3 条）
bash scripts/check-eol.sh; echo EOL=$?
```

## 5. 验收口径

| # | 查什么 | 通过标准 |
| :--- | :--- | :--- |
| 1 | PRD 两处 | `grep -n "PAID" "docs/_API 中转站 · 需求文档 V1.2.md"` 在 G-03 段落命中；`APPLIED` 在 G-07 段落命中；两处均带勘误标记；PRD 其它行 diff 为空 |
| 2 | 断言未改 | `git diff` 里 G-03/G-07 的 `t.Fatalf` 行零改动 |
| 3 | 日志已删 | `grep -c DEVIATION` 从 5 降到 3（G-02/G-05/G-09 保留，它们是真差异） |
| 4 | 回归 | G-03、G-07 用例 PASS；`EOL=0` |

## 6. 交付记录

派单 14:10，交付 14:12（31 turns / 96s）。

| 项 | 自报 | 独立复核 | 裁定 |
| :--- | :--- | :--- | :--- |
| PRD 勘误 | `:1393` `SUCCESS→PAID`、`:1470` `PENDING→APPLIED`，各带 `（勘误 ERR-03 / ERR-04，2026-10-29）` | `git diff` 该文件**只有这两行**，数值/结构/结论未动 | ✅ |
| 删过期日志 | 删掉 G-03、G-07 两行 `t.Logf("… DEVIATION …")`，注释改为「PRD 已按 ERR-03/ERR-04 勘误」 | `grep -c DEVIATION` 由 4 → **2**（G-05、G-09 保留，它们是真差异）。实现方纠正了我卡里的「5→3」——G-02 那行日志不含 DEVIATION 字样，本来就没计入 | ✅ |
| 断言未改 | `t.Fatalf` 两行零改动 | `git diff` 确认；我另外把两处**失败文案**里残留的「PRD text says SUCCESS / PENDING」改成「PRD corrected by ERR-03/ERR-04」（只改消息，不改判断） | ✅ |
| 矩阵/偏差 | `matrix-gwt` G-03/G-07 → ✅；`deviations` D-08/D-09 标关闭，门槛行 6/12 → 8/12 | 读 diff 确认；`go-live.md` 门槛 2 依据我同步更新 | ✅ |

自检：`go test ./internal/acceptance -run TestGWT_G` → `ok`（12/12 PASS）；`EOL=0`。

**遗留**：`deviations.md` 里 D-08/D-09 的「理由」列仍保留当时的原话，属历史记录，未改写。

