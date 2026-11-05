-- 教学班与开班权限（P2，docs/plan/01-information-architecture.md §6）。
-- 这一版只有社会班：运营开教师账号并授权「可开班」，老师开班、在自己班里开学员账号。
-- 学校班（school_id 非空）由学校管理员排班，随 P3 加接口。
ALTER TABLE accounts ADD COLUMN can_open_classes boolean NOT NULL DEFAULT false;

CREATE TABLE teaching_spaces (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    school_id text REFERENCES schools(id) ON DELETE CASCADE, -- 社会班为空
    opened_by text REFERENCES accounts(id) ON DELETE SET NULL,
    demo_account_id text REFERENCES accounts(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- 教师、学生身份以这里为准；school_members 里的 teacher / student 只是学校名册。
CREATE TABLE teaching_space_members (
    space_id text NOT NULL REFERENCES teaching_spaces(id) ON DELETE CASCADE,
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('teacher', 'student')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (space_id, account_id)
);
CREATE INDEX teaching_space_members_account ON teaching_space_members(account_id);

-- 演示账号多一个演示社会班：演示教师任教，演示学生和三名演示社会学员在班里。
ALTER TABLE demo_setups ADD COLUMN class_id text REFERENCES teaching_spaces(id);
UPDATE accounts SET can_open_classes = true
    WHERE id IN (SELECT persona_account_id FROM demo_personas WHERE perspective = 'teacher');
