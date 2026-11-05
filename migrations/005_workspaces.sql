-- 网页版每人一个 WorkSwarm 容器（P2，docs/plan/02-backend.md §10）。
-- 容器按登录账号开（演示账号切视角时仍是同一个容器）。端口一经分配就永久属于这个账号：
-- 容器意外退出后端口也不会转给别人，转发不可能落到别人的容器里。
CREATE TABLE workspaces (
    account_id text PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    port integer NOT NULL UNIQUE CHECK (port BETWEEN 1024 AND 65535),
    state text NOT NULL DEFAULT 'stopped' CHECK (state IN ('starting', 'ready', 'stopped', 'failed')),
    started_at timestamptz,
    last_seen_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workspaces_state ON workspaces(state);

-- agent.hrtiger.cn 上转发用的 cookie 只存哈希，挂在登录会话上：退出、改密、重置密码、停用账号
-- 让会话失效时，转发也一起失效。
ALTER TABLE sessions ADD COLUMN workspace_hash text UNIQUE;
