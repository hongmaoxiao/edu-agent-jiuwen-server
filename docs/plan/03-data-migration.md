# 03 数据迁移：新库、新表、一次性导入

## 1. 原则

- **新起一个 PostgreSQL 18 库**，由本仓的迁移脚本建表，跑在独立容器里（见后端架构 §3）。不连原智教云的库，不共享表，也不做双写。
- **按表建模。** 智教云把课程、实训、学生、作品、用量等核心数据放在一个大 JSON（`workspace.data`，后来拆进 `business_objects`），靠全局 revision 加行级 revision 防冲突。新库每种实体一张表，外键、唯一约束、事务都交给数据库。
- **迁内容，不迁人。** 默认只把课程内容（课程、章节、实训、指引、评分规则）和模型配置导进新库。学校、账号、作品、成绩在新系统里重新开通。原智教云的运营数据留在原系统，原系统以后单独运营。11-03 用户确认：**只迁课程**。

## 2. 新库表结构（首版）

只列 P1–P4 需要的表。测验、知识库到 P5 再加。

| 模块 | 表 | 说明 |
|---|---|---|
| account | `accounts` | 登录名、显示名、密码哈希、状态、是否平台运营、是否可建课 |
| | `identity_providers` | 外部身份（SSO、华为账号），P1 建空表 |
| | `sessions`、`refresh_tokens`、`model_tokens` | 只存哈希；可吊销 |
| | `login_attempts` | 限流 |
| org | `schools` | 学校，含城市、联系人 |
| | `school_members` | 账号在学校里的角色：school_admin / teacher / student |
| | `admin_classes`、`admin_class_members` | 行政班 |
| content | `courses` | 源课程：标题、描述、品类（FDE、AI 漫剧…）、所属学校或平台、作者 |
| | `course_versions` | 发布时生成的不可变快照（JSON），教学班只引用版本 |
| | `chapters`、`labs` | 编辑中的草稿；实训含场景、交付物、边界、指引步骤（JSON） |
| | `categories` | 项目页的品类卡片 |
| teaching | `teaching_spaces` | 教学班：学校、课程版本、学期、状态 |
| | `teaching_space_members` | 教师、助教、学生 |
| | `lab_openings` | 实训对整个班、某个行政班或某个学生开放 |
| learning | `lab_attempts` | 一次实训：学生、实训、客户端会话 id |
| | `submissions` | 提交的材料：回答、文件、步骤、学员指令、核查回答、客户端自报用量 |
| grading | `rubrics`、`rubric_versions` | 评分规则草稿和已确认版本 |
| | `evaluations` | AI 初评结果：分项、理由、改进建议、是否一次性完成 |
| | `grade_drafts`、`grade_publications` | 教师评阅草稿和发布记录 |
| | `appeals` | 申诉 |
| models | `models`、`model_prices` | 模型目录和按时段生效的单价 |
| metering | `usage_ledger` | 每次模型调用一行：账号、学校、教学班、课程、用途（智能体、AI 初评…）、预留量、实际用量、费用、状态 |
| | `budgets`、`budget_grants` | 学校预算、教学班额度、学生每日上限 |
| jobs | `jobs` | 后台任务 |
| ops | `audit_events`、`idempotency_keys` | 审计和幂等 |

这样约 35 张表，原来是 64 张。少掉的主要是：

- 写入回执和核对表（`*_write_receipts`、`credential_windows`、`education_write_receipts` 等）：由 `idempotency_keys` 和事务取代。
- 三套各算各的计费表（`learning_billing_*`、`grading_billing_receipts`、`grading_reservation*`、`grading_accounting_repairs`、`study_workspace_model_calls`、`ai_usage`）：合并进 `usage_ledger`。
- 旧实训形态（`learning_requests`、`learning_calls`、LangGraph 线程、`study_workspace_access`）：功能本身不迁。
- 测验 9 张、知识库 2 张：P5 再建。

## 3. 实体对照

| 智教云 | 新库 | 备注 |
|---|---|---|
| `course_content` + `course_content_versions` | `courses` + `chapters` + `labs` + `course_versions` | 原来章节内嵌在课程 JSON 里，拆成表 |
| state 里的 `courses`（课程 id = 教学班 id） | `teaching_spaces` 引用 `course_versions` | 不再把版本展开写回状态，教学班只存版本 id |
| state 里的 `labs`（类型 prompt/rag/coze/langgraph/harness/workswarm） | `labs` | 只迁 WorkSwarm 实训（现有 FDE 课程 51 个）。旧类型不迁（11-03 用户确认） |
| `scripts/lab_guides.json` + `prepare_workswarm_demo.py` 里的 STRUCTURED | `labs.guide`、`labs.scenario`、`labs.deliverables`、`labs.boundary` | 现在每次部署都要重跑 prepare 才能写进去，以后作为正式内容存库，教师在页面里编辑 |
| `grading_rubrics`（含「工具调用最小闭环」已确认的 6 项规则） | `rubrics` + `rubric_versions` | 其余 50 个实训目前没有规则 |
| `accounts`、`account_relations`（membership / class / teaching / enrollment） | `accounts`、`school_members`、`admin_class_members`、`teaching_space_members` | 默认不迁数据，只迁结构 |
| `teaching_spaces`、`teaching_space_roles` | `teaching_spaces`、`teaching_space_members` | |
| `course_item_openings`、`course_groups` | `lab_openings`、`categories` | |
| `artifacts`、`runs`、`grading_evaluations` | `lab_attempts`、`submissions`、`evaluations` | 默认不迁数据 |
| `ai_usage`、`agent_model_calls`、各计费回执 | `usage_ledger` | 默认不迁数据 |
| `models`、providers 配置 | `models`、`model_prices` | 迁模型清单和单价，不迁密钥 |
| `funding_accounts`、`lab_token_limits`、`school_quota` | `budgets`、`budget_grants` | |

## 4. 导入工具

`cmd/admin import-content`：

- 输入是一个导出目录，里面是 JSON 文件，不直接连原智教云的库。
- 导出目录由 edu-agent-jiuwen 仓库里的一个只读脚本生成，数据来源是该仓库 `platform/shared/seed.json`、`platform/scripts/lab_guides.json` 和 prepare 脚本里的结构化字段。也就是现在 agent.hrtiger.cn 线上的 FDE 内容，不是 practise.hrtiger.cn 的生产数据。
- 品类有两处来源，要先核对再定：客户端 `edu_course/frontend/ProjectCards.tsx` 里写死的 FDE、AI 漫剧两张卡片，以及 `ai-edu-platform` 的 `feat/redesign-categories` 分支 `shared/seed.json` 里的 `categories`。后者的 `lab_types` 仍列着旧实训类型，导入时要改成 WorkSwarm。
- 可以重复执行：同一个来源 id 只更新，不重复插入。已经被教师在新系统里改过的实训不覆盖，只报告差异。这修掉了现在 prepare 脚本每次部署都会覆盖线上修改的问题。
- 导入后输出核对报告：课程、章节、实训、指引步骤、交付物、规则的数量，以及逐项哈希。

原系统的学校、账号、作品、成绩不迁（11-03 用户确认）。

## 5. 切换步骤（P2）

1. Go 服务在 `api.agent.hrtiger.cn` 上线，导入课程内容，建演示学校、演示账号和三个演示身份。
2. 每人容器上线：新版客户端镜像读 Go 服务；旧的共用实例和 Python 平台先保留，作为回退。
3. 在 Grok 和 Mac 上跑完整闭环，与现有线上版本逐项对比：目录内容、指引、空输入框、提交、评分分项、一次性完成封顶。
4. agent.hrtiger.cn 切到「登录 → 每人容器」。先保留 Python 平台容器，观察一周无问题再下线。
5. 从 edu-agent-jiuwen 仓库删除 `platform/` 和平台相关的部署文件，留一份说明指向本仓。

切换前后要分别做数据备份，并演练一次回退：nginx 切回旧的共用实例，线上能恢复原状。
