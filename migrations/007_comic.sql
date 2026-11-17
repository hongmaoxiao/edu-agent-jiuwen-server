-- AI 漫剧制作工作台：项目、生成任务、媒体文件、创作点账本（docs/comic-studio.md）。
-- 项目内容（剧本、资产设定、分镜、时间轴、采用关系）是客户端维护的一份 JSON 文档，带版本号做乐观锁；
-- 任务、媒体由后台工作协程写入，单独成表，不和客户端的编辑互相覆盖。

CREATE TABLE comic_projects (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name text NOT NULL,
    doc jsonb NOT NULL DEFAULT '{}'::jsonb,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX comic_projects_account ON comic_projects(account_id, updated_at DESC) WHERE deleted_at IS NULL;

-- 一次生成（或解析、合成）一行。状态：queued → running → succeeded / partial / failed / cancelled。
-- estimate 是提交时扣的创作点；refunded 是退回的；两者都写进 comic_ledger，这里只是方便列表显示。
CREATE TABLE comic_tasks (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES comic_projects(id) ON DELETE CASCADE,
    kind text NOT NULL,
    target text NOT NULL DEFAULT '',
    label text NOT NULL DEFAULT '',
    provider text NOT NULL,
    model text NOT NULL,
    input jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'queued',
    provider_task_id text NOT NULL DEFAULT '',
    estimate integer NOT NULL DEFAULT 0,
    refunded integer NOT NULL DEFAULT 0,
    result jsonb,
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    simulated boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    next_check_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz
);
CREATE INDEX comic_tasks_project ON comic_tasks(project_id, created_at DESC);
CREATE INDEX comic_tasks_account ON comic_tasks(account_id, created_at DESC);
CREATE INDEX comic_tasks_open ON comic_tasks(next_check_at) WHERE status IN ('queued', 'running');

-- 生成或上传的文件。target 说明它是给谁的候选（如 asset:<id>/form:<id>/main、shot:<id>/video、line:<id>/audio）。
-- 文件放在 EDU_COMIC_MEDIA_DIR 下，path 是相对路径。
CREATE TABLE comic_media (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES comic_projects(id) ON DELETE CASCADE,
    task_id text REFERENCES comic_tasks(id) ON DELETE SET NULL,
    target text NOT NULL DEFAULT '',
    kind text NOT NULL,
    origin text NOT NULL DEFAULT 'generated',
    path text NOT NULL,
    mime text NOT NULL,
    bytes bigint NOT NULL,
    width integer NOT NULL DEFAULT 0,
    height integer NOT NULL DEFAULT 0,
    duration_ms integer NOT NULL DEFAULT 0,
    meta jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comic_media_project ON comic_media(project_id, target, created_at DESC);

-- 创作点账本：grant 加额度，charge 提交时扣，refund 失败退回。余额 = grant + refund − charge。
CREATE TABLE comic_ledger (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    task_id text REFERENCES comic_tasks(id) ON DELETE SET NULL,
    kind text NOT NULL CHECK (kind IN ('grant', 'charge', 'refund')),
    amount integer NOT NULL CHECK (amount > 0),
    note text NOT NULL DEFAULT '',
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comic_ledger_account ON comic_ledger(account_id, created_at DESC);
