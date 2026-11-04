-- 账号、学校与会话（P1）。权限判断见 internal/account/authz.go。
CREATE TABLE accounts (
    id text PRIMARY KEY,
    login text NOT NULL UNIQUE CHECK (login ~ '^[a-z0-9][a-z0-9._-]{2,63}$'),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 64),
    password_hash text NOT NULL,
    must_change_password boolean NOT NULL DEFAULT true,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    is_operator boolean NOT NULL DEFAULT false,
    can_create_courses boolean NOT NULL DEFAULT false,
    is_demo boolean NOT NULL DEFAULT false,
    password_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 外部身份（学校 SSO、华为账号），P5 才写入；先建表，后加不用改 accounts。
CREATE TABLE identity_providers (
    provider text NOT NULL,
    subject text NOT NULL,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);

CREATE TABLE schools (
    id text PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 100),
    city text NOT NULL DEFAULT '',
    is_demo boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE school_members (
    school_id text NOT NULL REFERENCES schools(id) ON DELETE CASCADE,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('school_admin', 'teacher', 'student')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (school_id, account_id, role)
);
CREATE INDEX school_members_account ON school_members(account_id);

-- 访问令牌和刷新令牌都只存哈希。generation 随每次刷新加一，旧刷新令牌再出现就整条会话作废。
CREATE TABLE sessions (
    id text PRIMARY KEY,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    client_app text NOT NULL CHECK (client_app IN ('web', 'desktop')),
    client_version text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    access_hash text NOT NULL UNIQUE,
    access_expires_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    generation integer NOT NULL DEFAULT 0,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_account ON sessions(account_id);

CREATE TABLE refresh_tokens (
    token_hash text PRIMARY KEY,
    session_id text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    generation integer NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- 管理操作的审计：谁、做了什么、对象是谁。不记录密码和令牌。
CREATE TABLE audit_events (
    id text PRIMARY KEY,
    account_id text,
    action text NOT NULL,
    target text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_created ON audit_events(created_at);
