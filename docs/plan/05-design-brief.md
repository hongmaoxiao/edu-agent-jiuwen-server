# 05 设计说明：智教云管理功能并入 jiuwen

交给 Claude Design。截图和代码都在下面写明了绝对路径，不需要另外附件。

## 1. 背景

jiuwen（WorkSwarm）是一个智能体桌面客户端，界面只有一条 72px 的窄图标侧栏，右边是页面内容。我们已经在里面做了「项目」页（品类 → 项目 → 实训）和实训指引面板。

现在要把另一个独立平台「智教云」的运营、学校管理员、教师、学生功能并进来。智教云原来是一个宽侧栏、绿色主题的网页后台。并入后**仍然只有 jiuwen 那一条图标侧栏**，视觉完全服从 jiuwen。

参考截图：

- jiuwen 项目页（要融入的界面）：`/workspace/box-dev/workspaces/temporary-development/edu-agent-jiuwen-server/docs/plan/assets/jiuwen-projects-page.png`
- jiuwen 实训指引面板（已上线，可以看出组件风格）：`/workspace/box-dev/workspaces/temporary-development/edu-agent-jiuwen-server/docs/plan/assets/jiuwen-lab-guide.png`
- 智教云运营总览（被并入的系统，只参考内容，不参考样式）：`/workspace/box-dev/workspaces/temporary-development/edu-agent-jiuwen-server/docs/plan/assets/zhijiaoyun-operator-dashboard.png`

jiuwen 的设计语言以代码为准：

- 颜色变量：`/workspace/box-dev/workspaces/temporary-development/edu-agent-jiuwen/client/jiuwenswarm/channels/web/frontend/src/styles/themes/default/light.css`（主色 `--color-action-primary: #2563eb`，页面底色 `--color-surface-page: #f5f7f9`）
- 圆角、字体：`…/frontend/src/styles/foundation.css`
- 组件：`…/frontend/src/components/ui/`（PageHeader、PageToolbar、Tabs、Dialog、FormDrawer、Select、Tag 等）
- 列表页范例：`…/frontend/src/components/SkillPanel/index.tsx`、`…/frontend/src/components/CronPanel/index.tsx`
- 「任务」页的第二列会话列表：`…/frontend/src/multi-session/sidebar/ConversationSidebar.tsx`
- 前端规则：`…/client/jiuwenswarm/channels/web/frontend/AGENTS.md`（不许写死颜色，只用语义变量；图标用 lucide；最低 Chrome 107）

智教云各页面要展示的内容可以看它的 Vue 源码：`/workspace/box-dev/workspaces/temporary-development/edu-agent-jiuwen/platform/frontend/src/components/`。页面与新位置的对应关系见同目录的 `01-information-architecture.md`。

## 2. 要设计的东西

### A. 外壳：按角色变化的侧栏和二级栏

侧栏顺序：任务、项目、**成绩**、**教学**、**管理**、专家、技能、扩展、实验，底部是设置、更新、**账号头像**。加粗的是新增入口，按角色显示：

- 学生：多「成绩」。
- 教师：多「教学」。
- 学校管理员、运营：多「管理」。运营在管理页的二级栏顶部有「全平台 / 选择学校」切换。
- 一个人有多个角色时，入口同时出现。
- **演示账号**多一个视角切换器：一个常驻的标签「演示 · 教师视角」，点开选运营、教师、学生。切换后侧栏立即变化，并停在该视角的默认页，不刷新、不重新登录。它是给客户现场演示用的，要让旁观的人一眼看懂「现在是谁在看」，切换过程不能突兀。请专门设计这个切换器和切换时的过渡。**普通账号的界面里完全没有这个切换器**，也不要给它留空位或灰掉的入口。

成绩、教学、管理三个页面都是「二级栏 + 内容区」。二级栏要和「任务」页的第二列在宽度、分组标题、选中态上一致，看起来就是同一个产品。二级栏顶部放当前范围（当前教学班或当前学校）。

请画出学生、教师、运营三种侧栏，二级栏展开和收起两种状态，以及演示账号的切换器。

### B. 登录

- 应用启动时的全屏登录页：账号、密码、登录按钮、错误提示、登录限流时的等待提示。
- 首次登录强制改密码。
- 「客户端版本过低，请升级」的整页提示。
- 账号头像菜单：显示名、角色、当前学校；个人信息与安全；退出登录。

网页版优先，桌面版用同一个设计。网页版登录后要等云端环境启动，需要一个「正在准备你的实训环境」的等待状态，以及「当前在线人数已满，请稍后」的排队状态。

### C. 关键页面（每页都要有空、加载、出错、无权限四种状态）

1. **管理 → 总览**：今日、本月、累计 Token；接入学校数、覆盖学生数、AI 实训调用数；Token 趋势（7 天、30 天）；模型调用分布；学校用量排行。运营看全平台，学校管理员看本校。这是数据最密集的一页，**请顺便给出图表的视觉规范**（折线、环形、条形的配色和坐标轴、图例、悬停提示的样式），后续所有图表照它做。
2. **管理 → 账号**：账号表格（姓名、登录名、角色、学校、状态、最近登录），筛选和搜索，新建账号抽屉，批量导入（上传表格 → 预览校验结果 → 确认导入 → 显示初始密码的下载方式），停用和重置密码。
3. **教学 → 作品评阅**：左边是作品列表（学生、实训、提交时间、AI 初评分、状态），右边是选中作品的详情：提交的文件、学员指令和步骤、AI 初评分项（得分、理由、引用、改进建议、「一次性完成」标记），教师逐项改分、写评语、保存草稿、发布。另有一个「批量复核」标签：按异常筛选（分数偏离、引用未核实），勾选后批量发布。
4. **教学 → 课程内容**：课程 → 章节 → 实训三级结构的编辑。实训的编辑项有标题、业务场景、交付物、边界，以及实训指引的步骤（每步有标题、目标、提示、完成标准 2–4 条、可选的核查问题）。有草稿和已发布版本，发布前要能预览「学生看到的样子」。
5. **成绩 → 我的作品**（学生）：自己的提交列表，点开看 AI 初评或教师发布的成绩，从这里发起申诉。

其余页面（院校、预算、模型、用量、评阅运行、评分规则、成绩册、申诉）用上面这几页定下来的模式就能做，这一轮不用画。

## 3. 约束

- **只用 jiuwen 的语义颜色变量**，不引入智教云的绿色。状态色（成功、警告、错误）也用 jiuwen 已有的。
- 宽度要覆盖 1920、1440、1280，以及桌面客户端的最小窗口 1100×720（`client/jiuwenswarm/channels/desktop/electron/main.cjs:2578`）。1280 以下二级栏可以收起成图标或抽屉。
- 表格密度和 jiuwen 现有列表一致，不要做成大卡片网格。数据多的页面以表格为主。
- 不改动「任务」「项目」页面和实训指引面板现在的样子。
- 中文界面，文案简短；按钮用动词。

## 4. 交付物

- 可点击原型：三种角色的侧栏、登录流程，以及第 2.C 节的五个页面。
- 标注：尺寸、间距、用到的颜色变量和组件名，以及二级栏的收起规则。
- 图表视觉规范。
- 状态总表：每页的空、加载、出错、无权限状态。

设计稿如果和代码现状对不上（例如尺寸、断点），实现时以代码为准，差异由实现方记录。
