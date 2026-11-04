# 04 客户端接入：jiuwen 怎么装下管理功能

路径缩写：`<C>` = `edu-agent-jiuwen/client/jiuwenswarm`，`<F>` = `<C>/channels/web/frontend/src`。

## 1. 现状与限制

- 侧栏在 `<F>/components/SessionSidebar/index.tsx`。插件项来自 `GET /api/application-plugins`，图标写死为 `PluginIcon`（:124）。
- `<F>/applicationPlugins/ApplicationPluginOutlet.tsx:110` 按 `plugin_id` 取组件，不看 contribution 的 `component` 字段，也不传 props。**一个插件只能有一个页面。**
- 插件页拿到侧栏以外的全部宽度，没有二级栏，切走就卸载。
- 插件列表在网关连上后只拉一次，前端没有暴露刷新方法，不能随登录、角色变化。
- Tailwind 只扫描 `src/**`，插件目录里写 Tailwind 类不生效。现有插件用自写 CSS 加 `var(--color-*)`。
- 没有图表库。依赖里只有 mermaid、@xyflow/react、exceljs。
- jiuwen 自带的「登录」是华为账号，用来领免费模型额度，没有业务角色，不能拿来当平台账号。

## 2. 宿主改动（尽量少，集中在插件插槽）

宿主改动越少，以后合并 openJiuwen 上游越省事。下面每一项都是小改，并配测试：

| 改动 | 位置 | 内容 |
|---|---|---|
| 多页面 | `ApplicationPluginOutlet.tsx` | 按 `contribution.component` 取组件，把 contribution 作为 prop 传给页面 |
| 侧栏图标 | `FrontendContribution`、`SessionSidebar` | 加可选字段 `icon`（lucide 图标名），缺省仍是 `PluginIcon` |
| 按角色显示 | `applicationPlugins` | 插件可导出 `applicationPluginNavFilter(contribution) => boolean`，宿主渲染侧栏时调用；插件状态变化时通过一个事件让侧栏重算 |
| 全局登录门 | `App.tsx` | 新插槽 `applicationPluginAppGate`：插件声明需要登录时，在整个应用外层先渲染登录页，登录成功后再渲染原界面 |
| 账号菜单 | 侧栏底部 | 新插槽 `applicationPluginAccountMenu`，放头像、个人信息、当前学校、退出 |
| 样式 | `tailwind.config.js` | `content` 加上 `../../../extensions/*/frontend/**` |

这些插槽和已有的 `HeaderAction`、`ComposerPanel`、`WorkContextAction` 是同一套机制。整理好以后可以考虑提交给 openJiuwen 上游。

## 3. 插件结构

| 插件 | 内容 |
|---|---|
| `edu_course`（现有） | 项目页、实训指引、去实训、提交评测。P2 改为调 Go 服务，并把提交结果从 localStorage 改存服务端 |
| `edu_platform`（新） | 登录门、账号菜单、令牌管理、Go API 客户端（由 OpenAPI 生成类型），以及「成绩」「教学」「管理」三个侧栏页面和它们的二级栏 |

两个插件共用 `edu_platform` 提供的 API 客户端和当前账号状态。

页面组件优先用宿主的 `<F>/components/ui`（PageHeader、PageToolbar、Tabs、Dialog、FormDrawer、Select 等），列表页照 `SkillPanel`、`CronPanel` 的写法。图表要新增一个依赖，在设计稿出来后再选（见设计说明）。

## 4. 登录与令牌

- 启动后，`edu_platform` 的登录门检查有没有刷新令牌。没有就显示登录页；有就换访问令牌，然后进入原界面。
- **访问令牌只在前端内存里。** 前端直接用 HTTPS 调 Go 服务，带 `Authorization: Bearer`。
- **刷新令牌**：
  - 桌面版：经 preload 暴露的一个 IPC，用 Electron `safeStorage` 加密存盘。要在 `<C>/channels/desktop/electron/preload.cjs` 和 `main.cjs` 各加几行。
  - 网页版：Go 服务在自己的域名下设 HttpOnly cookie，前端不接触。
- **模型令牌**：登录后由前端向 Go 申请，通过本机 WebSocket 方法 `edu.gateway.configure` 交给 Python 网关，写进智能体的模型配置。退出登录时一并吊销、删除。
- **本机 Python 不持有平台身份。** 开实训目录、采集作品这些本机操作照旧走 WebSocket，采集结果交回前端，由前端提交给 Go。现在的 `edu.submit` → `edu.evaluate` 已经是这种分工。
**威胁说明**：上面的做法能保证平台令牌不出现在智能体的环境变量和配置文件里，但在桌面版上**挡不住智能体主动去读**：

- 智能体和客户端是同一个系统用户，智能体的 bash 有完整权限。
- Windows 的 `safeStorage` 基于 DPAPI，同一用户的任何进程都能解密。Linux 没有钥匙串时，退化成近似明文。macOS 钥匙串能否被别的进程静默读取，还要实测。
- 学生账号泄露的影响有限，只涉及本人的作品和成绩。运营和学校管理员的令牌能动全平台或全校的数据，如果被提示注入的智能体读走，后果大得多。

11-03 用户确认运营和学校管理员**可以**用桌面版。缓解办法是高风险写操作要求二次确认密码，以及高权限账号的刷新令牌缩短到 12 小时（见后端架构 §5）。这两条只能减小损失，挡不住智能体在确认后的 5 分钟内冒用。

实现前要在三个平台上实测 `safeStorage` 的实际保护范围。

- 网页版 agent.hrtiger.cn 现在的固定账号登录页（`deploy/agent-login/`）到 P2 下线，由新账号登录取代。

## 5. 打包与部署

- 桌面版：`client/scripts/build-electron-exe.*` 打包，前端用 `ELECTRON=true vite build`。Go 服务地址作为构建配置写进前端，另留一个高级设置可以改地址，方便私有部署。
- 网页版：`deploy/Dockerfile.client` 继续保留在客户端仓库，只部署 WorkSwarm。平台容器、数据库、密钥的部署移到本仓 `deployments/`。
- 客户端版本号每次请求都带给服务端，低于最低版本时提示升级（见后端架构 §2）。

## 6. 网页版优先：每人一个云端 WorkSwarm

11-03 用户确认网页版优先，和 10-29 选定的正式形态一致。

- agent.hrtiger.cn 的登录页由 nginx 直接提供，是一个静态页，调 `api.agent.hrtiger.cn` 登录。Go 服务把会话 cookie 设在 `agent.hrtiger.cn` 域下，`api.agent.hrtiger.cn` 也能收到，并带 HttpOnly、Secure、SameSite=Lax。
- 登录后，broker 为这个人开好容器。nginx 按会话把 agent.hrtiger.cn 的请求转发到他自己的容器（见后端架构 §10）。
- 容器里的 jiuwen 前端用同一个 cookie 调 `api.agent.hrtiger.cn`（`credentials: 'include'`，Go 端只允许这个来源跨域）。网页版因此不需要在前端存刷新令牌。
- 每个容器有自己的智能体配置，模型令牌写在里面，用量按人分开。
- 现在的固定账号登录页（`deploy/agent-login/`）和共用实例在 P2 切换完成后下线。

桌面版放到 P5 正式发布。前端代码和网页版是同一套，区别只在登录门：桌面版要存刷新令牌（本节以上各条），网页版用 cookie。
