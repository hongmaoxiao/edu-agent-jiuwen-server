-- 课程内容（SRV-003）：品类 → 课程 → 章节 → 实训，以及实训的评分规则。
-- source_id 是导入来源里的 id，重复导入按它更新；imported_hash 判断来源内容有没有变；
-- edited_at 在新系统里编辑过就会设上，之后的导入不再覆盖这一行。
CREATE TABLE categories (
    id text PRIMARY KEY,
    title text NOT NULL,
    eyebrow text NOT NULL DEFAULT '',
    summary text NOT NULL DEFAULT '',
    flow jsonb NOT NULL DEFAULT '[]',
    status text NOT NULL DEFAULT 'live' CHECK (status IN ('live', 'coming_soon', 'hidden')),
    position integer NOT NULL DEFAULT 0
);

CREATE TABLE courses (
    id text PRIMARY KEY,
    source_id text UNIQUE,
    category_id text NOT NULL REFERENCES categories(id),
    title text NOT NULL,
    description text NOT NULL DEFAULT '',
    imported_hash text NOT NULL DEFAULT '',
    edited_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE chapters (
    id text PRIMARY KEY,
    course_id text NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    source_id text,
    position integer NOT NULL,
    title text NOT NULL,
    content text NOT NULL DEFAULT '',
    imported_hash text NOT NULL DEFAULT '',
    edited_at timestamptz,
    UNIQUE (course_id, source_id)
);

CREATE TABLE labs (
    id text PRIMARY KEY,
    course_id text NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    chapter_id text REFERENCES chapters(id) ON DELETE SET NULL,
    source_id text,
    position integer NOT NULL,
    title text NOT NULL,
    level text NOT NULL DEFAULT '',
    duration_minutes integer NOT NULL DEFAULT 0,
    description text NOT NULL DEFAULT '',
    objectives jsonb NOT NULL DEFAULT '[]',
    scenario text NOT NULL DEFAULT '',
    deliverables jsonb NOT NULL DEFAULT '[]',
    boundary text NOT NULL DEFAULT '',
    -- 指引步骤：[{id,title,goal,hint?,checks[],question?}]，客户端的实训指引面板按它显示。
    guide_steps jsonb NOT NULL DEFAULT '[]',
    imported_hash text NOT NULL DEFAULT '',
    edited_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (course_id, source_id)
);
CREATE INDEX labs_course ON labs(course_id, position);

-- 评分规则只追加版本，不改已确认的版本：评过的作品要能对上当时用的规则。
CREATE TABLE rubrics (
    id text PRIMARY KEY,
    lab_id text NOT NULL UNIQUE REFERENCES labs(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE rubric_versions (
    id text PRIMARY KEY,
    rubric_id text NOT NULL REFERENCES rubrics(id) ON DELETE CASCADE,
    version integer NOT NULL,
    title text NOT NULL,
    body jsonb NOT NULL,
    max_score numeric NOT NULL CHECK (max_score > 0),
    hash text NOT NULL,
    -- 为空表示来自导入；P3 起教师在新系统里确认的版本记确认人。
    confirmed_by text REFERENCES accounts(id),
    confirmed_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (rubric_id, version)
);
