# 云枢 API · Logo 资产包

本目录为《API 中转站 · 需求文档 V1.2》§1.4 品牌规范的**落地资产**，全部从 Demo 原型页面截取并矢量化复刻。

- 来源页面：`https://9dea1cbfc42541d1967ef6b87aeb18de.app.workbuddy.host`
- 权威定义：`assets/core.js` 中 `logomark()` 的 SVG path，样式取自 `assets/styles.css` 的 `.logo` / `.logo .mark` 规则
- 设计基准几何：容器 28×28、圆角 9、图标 15×15、描边 1.9、渐变 `linear-gradient(145deg,#5B52F0,#3B32C9)`、投影 `0 3px 9px rgba(79,70,229,.32)`

## 一、目录结构

```text
assets/logo/
├── preview.png                 资产总览图（一眼核对全部版本）
├── favicon.ico                 站点图标（16/24/32/48/64/128/256 多尺寸内嵌）
├── svg/                        矢量源文件（首选，任意缩放不失真）
│   ├── yunshu-mark.svg         标准 Mark：品牌渐变圆角底 + 白色枢形
│   ├── yunshu-mark-elevated.svg  标准 Mark + 投影（大尺寸展示用）
│   ├── yunshu-mark-line.svg    纯线条单色（currentColor，需内联使用）
│   ├── yunshu-mark-line-white.svg  纯线条白色（深色底）
│   ├── yunshu-lockup-light.svg   横版组合标（深色文字，亮底）
│   ├── yunshu-lockup-dark.svg    横版组合标（白色文字，暗底）
│   └── favicon.svg             浏览器标签图标
├── png/                        位图（有尺寸刚性要求时使用）
│   ├── yunshu-mark-{16,32,48,64,128,256,512,1024}.png   透明底图标
│   ├── yunshu-mark-shadow-{512,1024}.png                带投影展示版（文件名=Mark 本体边长）
│   ├── yunshu-mark-brand-{192,512}.png                  满铺渐变（PWA / 应用图标）
│   ├── apple-touch-icon.png    180×180 满铺渐变（iOS 不支持透明）
│   ├── favicon-{16,32,48}.png  站点图标 PNG 备选
│   └── yunshu-lockup-{light,dark}@{1x,2x,4x}.png        横版组合标位图
└── source/                     原始网页截取存档（@4x，含浏览器渲染的投影）
    ├── demo-header-logo@4x.png    线上顶部导航 Logo（Mark + 文字）
    ├── demo-header-mark@4x.png    线上顶部导航 Mark 单元
    ├── demo-footer-logo@4x.png    线上页脚 Logo
    └── demo-landing-fold@4x.png   首屏折叠区截图（含 Logo 上下文）
```

## 二、使用约定

1. **优先用 SVG**。位图仅在目标环境不支持 SVG（部分邮件客户端、旧版富文本）时使用；不要低分辨率放大。
2. **尺寸阶梯**：16 → 32 → 48 → 64 → 128 → 256 → 512，按目标渲染尺寸取最近档，不做非整数缩放。
3. **最小尺寸**：Mark 单独使用不小于 16 px；横版组合标不小于 88 px 宽（低于此文字不可读，改用纯 Mark）。
4. **留白**：Logo 四周至少保留 `0.5 × 容器边长` 的安全边距，不与其他元素贴边。
5. **带投影版的画布比 Mark 本体大**：每边预留 `10/28` 的阴影留白，因此 `shadow-512` 实际画布 878×878、`shadow-1024` 为 1756×1756，只有本体是命名所标的边长。需要正方形定尺画布时请用 `yunshu-mark-{n}.png` 或 `brand-{n}.png`。
6. **`source/` 里的页脚截图记录的是原型缺陷**：原型 `.site-foot a{display:block}` 覆盖了 `.logo{display:flex}`，页脚 Logo 呈上下堆叠。该目录仅作取证，不可作为版式参考，横向形态见 `lockup-*`。
7. **`yunshu-mark-line.svg` 使用 `currentColor`**，只能内联进 HTML（`<svg>` 直接嵌入）才能继承文字色；以 `<img>` 引用会渲染为黑色，此时请改用 `yunshu-mark-line-white.svg` 或 `yunshu-mark.svg`。

## 三、页面接入片段

```html
<link rel="icon" href="/assets/logo/svg/favicon.svg" type="image/svg+xml">
<link rel="icon" href="/assets/logo/png/favicon-32.png" sizes="32x32" type="image/png">
<link rel="apple-touch-icon" href="/assets/logo/png/apple-touch-icon.png">
<meta name="theme-color" content="#4F46E5">
```

顶部导航与页脚统一使用「Mark + 文字标」的单一链接单元，禁止只显示图形或只显示文字：

```html
<a class="logo" href="/" aria-label="云枢 API 首页">
  <img src="/assets/logo/svg/yunshu-mark.svg" width="28" height="28" alt="">
  <span>云枢 API</span>
</a>
```

## 四、禁止项（与 PRD §1.4 一致）

1. 不得改动枢形语义——中心圆环与八向短线是「枢」的图形转译，只能整体等比缩放，不可删减射线、不可把圆环改为实心点。
2. 不得替换品牌渐变色值，不得对 Mark 做旋转、倾斜、描边加粗、投影二次叠加。
3. 不得将 Logo 资源外链至 CDN 或第三方图床，须自托管于本项目静态资源目录。
4. 不得单独把文字「云枢 API」用非指定字体排版成 Logo（字体栈：Inter → PingFang SC → HarmonyOS Sans SC → Microsoft YaHei）。

## 五、复现方式

```bash
# 1) 重新从 Demo 网页截取并渲染 1024 基准图（需要 Playwright + Chromium）
python tools/capture_logo_step1.py

# 2) 由基准图生成全部 PNG / ICO / SVG 资产（需要 Pillow）
python tools/build_logo_assets.py

# 3) 重新生成资产总览图
python tools/_sheet.py
```

渲染中间产物位于 `tools/logo-render/`（`mark-flat-1024.png`、`line-white-1024.png`），不属于交付资产。
