# 设计稿交接：智教云管理功能并入 jiuwen

按 [05-design-brief.md](../05-design-brief.md) 出的设计稿。导出自 Claude Design 画布 https://claude.ai/artifact/1DxCHsyGyFE2aGAxuvmUCf （2026-11-04，画布版本 `1793771158-7269`）。

**实现以本目录为准，不需要打开画布。** 本目录和已发布画布逐字节一致，核对记录见文末。画布是私有的：只有带 Artifact 工具的会话能读（出稿的会话 11-04 读取成功）；Claude Docs 工具管的是文档，读不到这个画布，会报无权限。要在浏览器里看运行效果，需要用户先从画布的 Share 菜单分享。

## 文件

| 文件 | 内容 | 以它为准的部分 |
|---|---|---|
| `Main.dc.html` | 可点击原型：登录接入、按角色变化的侧栏、二级栏（展开 / 收起 / 窄窗浮层）、演示视角切换与过渡、账号菜单 | 外壳状态逻辑、视角切换时序、二级栏各入口的分组和顺序 |
| `Shell.dc.html` | 学生、教师、教师 + 学校管理员、运营四种侧栏；二级栏三态；四种宽度；尺寸与变量 | 尺寸、收起规则、断点 |
| `Switcher.dc.html` | 演示视角切换器标注、普通账号与演示账号的账号菜单、过渡分解 | 切换器外观、菜单内容、过渡时序 |
| `Login.dc.html` | 登录、密码错误、登录限流、首次改密、版本过低、准备实训环境、排队 | 登录流程的文案和状态 |
| `Overview.dc.html` | 管理 → 总览（全平台 / 本校 / 社会班 / 演示） | 指标、图表布局 |
| `Accounts.dc.html` | 管理 → 账号：表格、筛选、批量选择、新建抽屉、四步批量导入、重置密码、停用 | 表格列、导入流程、二次确认密码 |
| `Review.dc.html` | 教学 → 作品评阅：逐份评阅、批量复核 | 评阅页结构、改分交互 |
| `Course.dc.html` | 教学 → 课程内容：课程树、实训编辑、指引步骤编辑、学生视图预览、发布 | 指引编辑的校验（完成标准 2–4 条） |
| `MyWorks.dc.html` | 成绩 → 我的作品：提交列表、成绩详情、申诉 | AI 初评与教师成绩的区分 |
| `Charts.dc.html` | 图表视觉规范 | 所有图表 |
| `States.dc.html` | 五个页面的空、加载、出错、无权限 | 状态文案和处理方式 |
| `Spec.dc.html` | 组件对应、样例、字号、间距、新增变量 | 组件选用 |
| `jiuwen.css` | 设计稿共用样式 | 见下文，**不要引入前端** |
| `canvas.json` | 画布索引（画板位置、分页、便签） | — |

先看 `Main.dc.html` 的 `<script>` 里的 `ROLES`、`RAIL`、`SEC`：侧栏入口、二级栏分组、各视角的默认页都在这里。各页面的数据都在各自文件的 `<script>` 顶部。

## 怎么用

- `.dc.html` 是 Claude Design 的组件格式，引用的 `./support.js` 是画布运行时，不在导出里。要看运行效果，打开上面的画布链接。读源码就能拿到结构、尺寸、文案和状态逻辑。
- `jiuwen.css` 只为设计稿服务。里面的变量名和取值抄自前端 `styles/themes/default/light.css` 和 `foundation.css`，`.jw-*` 类是设计稿自己的写法。实现时用前端现有组件（PageHeader、PageToolbar、Tabs、FormDrawer、Dialog、Select、Tag 等，对应关系见 `Spec.dc.html`）和主题变量，不要把这个文件带进前端。
- `--color-chart-*` 是**唯一新增的颜色变量**，要加进主题文件。浅色取值已在白底上做过色盲校验，深色（dark.css）取值还没定，要在深色表面上重新校验。
- 图标在设计稿里用 CSS mask 画，实现用 lucide 同名图标：任务 CirclePlus、项目 House、成绩 Award、教学 Presentation、管理 Building2、二级栏收起 PanelLeft、总览 LayoutDashboard、用量 ChartColumn、院校 GraduationCap、账号 User、教学班 Users、预算 Wallet、模型 Cpu、评阅运行 Activity、系统 Server、课程内容 BookOpen、评分规则 ListChecks、作品评阅 ClipboardCheck、成绩册 Table、申诉 MessageSquareWarning、我的作品 Folder、历史成绩 History。侧栏原有入口继续用现在的图标资源。
- 尺寸、断点和代码现状对不上时，以代码为准，差异由实现方记录（设计说明 §4）。

## 出稿后新定的规则（设计稿已体现）

- 普通账号不显示演示视角切换器，头像也没有外环；只有演示账号显示（11-04）。
- 社会学员与社会班（11-04）：教学班可以不属于学校。运营的范围切换是「全平台 / 学校 / 社会班」；账号页学校可为空；总览排行学校和社会班一起排。规则见 [信息架构](../01-information-architecture.md) §6。
- 演示账号除了演示学校，再带一个演示社会班（11-04）。

## 设计上的取舍

- 演示标签竖排放在侧栏底部、头像上方：72px 宽的侧栏放不下横排的「演示 · 教师视角」。切换时屏幕中央的名牌负责让旁观者看清「现在是谁」。
- 窗口宽度低于 1280 时，二级栏默认收起，展开时以浮层盖在内容上，复用 ConversationSidebar 的 `is-floating` 和遮罩。
- 本轮没画的二级页面（院校、预算、模型、用量、评阅运行、系统、教学班、评分规则、成绩册、申诉、历史成绩、我的用量）在原型里只显示一句「沿用哪个已画页面的模式」。
- 「任务」「项目」页和实训指引面板保持现状，原型里只是简化的示意。

## 数据来源

- 真实内容：评分项和分值、指引步骤、实训场景和交付物来自客户端仓库 `platform/scripts/prepare_workswarm_demo.py`；累计 Token、接入学校数、学生数、调用数和模型占比来自智教云运营总览截图。
- 示意数据：学校名、人名、今日和本月数字、排行、作品和成绩。

## 没做的

- 没有逐页截图检查过画面效果。
- 没出深色模式。
- 只有中文文案；前端规则要求中英文都进语言资源，英文由实现方补。

## 核对记录

2026-11-04，出稿的 Claude Code 会话用 Artifact 工具从画布下载了版本 `1793771158-7269` 的全部 14 个文件，逐个和本目录的文件做 `cmp` 比对：14 个全部一致，0 个不一致。画布服务端给出的 sha256 也和下表相同。README.md 是本目录另写的说明，不在画布里。

| 文件 | 字节数 | sha256 |
|---|---|---|
| `canvas.json` | 3792 | `38822ab452d9a954ab59ba26cc2206279189c54d11e31e982375bae2d115b7cd` |
| `jiuwen.css` | 53795 | `8572e22abd17b918d5080aa8605911a94873f1c5d803e1d5942e640d3acd24e0` |
| `Main.dc.html` | 30546 | `41d0348a336d71d9a6755598e48e3f7c677492b9ef88144fdd5133842739ce18` |
| `Shell.dc.html` | 16098 | `46a61a15ff30816edcd4f8ee773b09b808b8aa55b72379fd290425734a51ba3c` |
| `Switcher.dc.html` | 18521 | `d46bc6afe3a949833283af0d19000c34de522dab11e881c3116a4223de8de230` |
| `Login.dc.html` | 16518 | `9b2741e56ea91626c33ed64ed64f3696c7c1c6d5452f50c94cda4236e84e0a4c` |
| `Overview.dc.html` | 18442 | `4b00a6629abf9e30a4cfba7e465abde30dc7e002ff3ec5364c64f43adefa6cb8` |
| `Accounts.dc.html` | 29908 | `3e7c425f4390bcee304185673a0f9acb3aca32df552057eacf3dc1eca03edab2` |
| `Review.dc.html` | 26842 | `84da178d827f79b1023630dab17374335c3a38d490696d657a7dcf3ead051c94` |
| `Course.dc.html` | 25968 | `faad5a386b0741b0b9afb06a9fea9927f68c389b6f912a5110532d4123da318c` |
| `MyWorks.dc.html` | 13225 | `bca02daf16e19186bdcf4acc8d7e0eddd3b40e4b86cf81968beb91544b69cace` |
| `Charts.dc.html` | 18991 | `f67716882fa83893418862eb66feead0d6e0b035336b8b993f48ed3e51d0c021` |
| `States.dc.html` | 10771 | `b486cf4acd8d17492271c448ab4c888b6346a29923e5b3e157964d77b067632e` |
| `Spec.dc.html` | 12817 | `5c3e1991e3162488f7e1f217a23dd13161c5b92e60c9774d4409410f847a8937` |

以后画布再改，要重新导出并更新这张表；表里的值和画布对不上时，以画布最新版本为准重新导出。
