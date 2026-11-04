-- 演示账号与视角切换（SRV-002，docs/plan/01-information-architecture.md §5）。
-- 一个演示账号带一所演示学校和三个演示身份；演示期间新建的学校、账号都记在它名下，重置时一起删掉。
ALTER TABLE accounts ADD COLUMN demo_account_id text REFERENCES accounts(id) ON DELETE CASCADE;
ALTER TABLE schools ADD COLUMN demo_account_id text REFERENCES accounts(id) ON DELETE CASCADE;

CREATE TABLE demo_setups (
    account_id text PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    school_id text NOT NULL REFERENCES schools(id)
);

-- 演示身份不能直接登录（密码哈希为 '!'），只能经 POST /v1/session/perspective 使用。
CREATE TABLE demo_personas (
    demo_account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    perspective text NOT NULL CHECK (perspective IN ('operator', 'teacher', 'student')),
    persona_account_id text NOT NULL UNIQUE REFERENCES accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (demo_account_id, perspective)
);

-- 演示会话当前用哪个演示身份；为空就是登录账号本人。
ALTER TABLE sessions ADD COLUMN acting_account_id text REFERENCES accounts(id) ON DELETE SET NULL;
ALTER TABLE audit_events ADD COLUMN acting_account_id text;
