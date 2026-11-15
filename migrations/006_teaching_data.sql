-- 教学数据：模型、用量、预算、作品与评分、申诉（docs/plan/03-data-migration.md §2 的精简版）。
-- 每张表都带 demo_account_id：演示账号的数据挂在演示账号名下，真实账号的查询一律排除；
-- 演示数据在「重置演示」时按当天日期重新生成（internal/account/demodata.go）。

-- 学校的展示信息；演示学校可以和真实学校同名，同名只在同一场演示内不允许。
ALTER TABLE schools ADD COLUMN kind text NOT NULL DEFAULT '';
ALTER TABLE schools ADD COLUMN contact text NOT NULL DEFAULT '';
ALTER TABLE schools DROP CONSTRAINT schools_name_key;
CREATE UNIQUE INDEX schools_real_name ON schools(name) WHERE demo_account_id IS NULL;
CREATE UNIQUE INDEX schools_demo_name ON schools(demo_account_id, name) WHERE demo_account_id IS NOT NULL;

-- 教学班学的课程和学期。
ALTER TABLE teaching_spaces ADD COLUMN course_id text REFERENCES courses(id) ON DELETE SET NULL;
ALTER TABLE teaching_spaces ADD COLUMN term text NOT NULL DEFAULT '';

-- 模型目录和单价（元 / 百万 Token）。
CREATE TABLE models (
    id text PRIMARY KEY,
    name text NOT NULL,
    provider text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    input_price numeric NOT NULL DEFAULT 0,
    output_price numeric NOT NULL DEFAULT 0,
    position integer NOT NULL DEFAULT 0,
    demo_account_id text REFERENCES accounts(id) ON DELETE CASCADE
);

-- 模型用量：一行是某天的一组调用合计。实时计量以后一次调用写一行（calls = 1）；
-- 迁移来的历史数据没有学生明细，只记到学校（account_id 为空）。
CREATE TABLE usage_records (
    id bigserial PRIMARY KEY,
    day date NOT NULL,
    account_id text REFERENCES accounts(id) ON DELETE CASCADE,
    school_id text REFERENCES schools(id) ON DELETE CASCADE,
    class_id text REFERENCES teaching_spaces(id) ON DELETE CASCADE,
    lab_id text REFERENCES labs(id) ON DELETE SET NULL,
    model_id text REFERENCES models(id) ON DELETE SET NULL,
    calls integer NOT NULL CHECK (calls >= 0),
    input_tokens bigint NOT NULL CHECK (input_tokens >= 0),
    output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
    cost numeric NOT NULL DEFAULT 0,
    demo_account_id text REFERENCES accounts(id) ON DELETE CASCADE
);
CREATE INDEX usage_records_day ON usage_records(demo_account_id, day);
CREATE INDEX usage_records_account ON usage_records(account_id, day);

-- 每月 Token 额度：给学校，或给社会班（运营直接批）。
CREATE TABLE budgets (
    id text PRIMARY KEY,
    school_id text REFERENCES schools(id) ON DELETE CASCADE,
    class_id text REFERENCES teaching_spaces(id) ON DELETE CASCADE,
    monthly_tokens bigint NOT NULL CHECK (monthly_tokens > 0),
    demo_account_id text REFERENCES accounts(id) ON DELETE CASCADE,
    CHECK ((school_id IS NULL) <> (class_id IS NULL)),
    UNIQUE (school_id),
    UNIQUE (class_id)
);

-- 实训作品：提交的文件、学员指令与核查回答、AI 初评（逐项）、教师成绩（逐项）和评语。
-- ai_items：[{title,max,score,reason,refs:[{text,verified}],tip,capped}]；teacher_scores 与之一一对应。
CREATE TABLE submissions (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    class_id text REFERENCES teaching_spaces(id) ON DELETE CASCADE,
    lab_id text NOT NULL REFERENCES labs(id) ON DELETE CASCADE,
    submitted_at timestamptz NOT NULL,
    files jsonb NOT NULL DEFAULT '[]',
    steps jsonb NOT NULL DEFAULT '[]',
    one_shot boolean NOT NULL DEFAULT false,
    flags jsonb NOT NULL DEFAULT '[]',
    ai_items jsonb NOT NULL DEFAULT '[]',
    ai_total numeric NOT NULL DEFAULT 0,
    teacher_scores jsonb,
    teacher_total numeric,
    comment text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'draft', 'published')),
    reviewer_id text REFERENCES accounts(id) ON DELETE SET NULL,
    published_at timestamptz,
    demo_account_id text REFERENCES accounts(id) ON DELETE CASCADE,
    UNIQUE (account_id, lab_id, class_id)
);
CREATE INDEX submissions_class_lab ON submissions(class_id, lab_id);

-- 申诉：每份作品一次，针对某一评分项。
CREATE TABLE appeals (
    id text PRIMARY KEY,
    submission_id text NOT NULL UNIQUE REFERENCES submissions(id) ON DELETE CASCADE,
    item text NOT NULL,
    reason text NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered')),
    reply text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    answered_at timestamptz,
    demo_account_id text REFERENCES accounts(id) ON DELETE CASCADE
);
