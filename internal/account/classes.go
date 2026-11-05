package account

import (
	"context"
	"errors"
	"fmt"

	"edu-agent-jiuwen-server/internal/platform"

	"github.com/jackc/pgx/v5"
)

// Social classes (docs/plan/01-information-architecture.md §6): operators open teacher accounts and allow them to
// open classes; a teacher opens a class and the student accounts in it, resets their passwords and removes them.
// Disabling accounts stays with operators.

type ClassView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsDemo   bool   `json:"is_demo"`
	Role     string `json:"role,omitempty"` // the caller's role in the class
	Students int    `json:"students"`
}

type ClassMember struct {
	Account AccountView `json:"account"`
	Role    string      `json:"role"`
}

type NewStudent struct {
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
}

type CreatedStudent struct {
	Member          ClassMember `json:"member"`
	InitialPassword string      `json:"initial_password"`
}

// MaxStudentsPerRequest bounds one batch; each initial password costs a PBKDF2 hash.
const MaxStudentsPerRequest = 200

// CreateAccount lets an operator open an account outside any school, typically a teacher of social classes.
func (s *Service) CreateAccount(ctx context.Context, actor Roles, login, displayName string, canOpenClasses, canCreateCourses bool) (AccountView, string, error) {
	if !actor.Operator {
		return AccountView{}, "", ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AccountView{}, "", err
	}
	defer tx.Rollback(ctx)
	id, password, err := insertAccount(ctx, tx, login, displayName, actor.DemoOwner)
	if err != nil {
		return AccountView{}, "", err
	}
	if _, err := tx.Exec(ctx, "UPDATE accounts SET can_open_classes=$2,can_create_courses=$3 WHERE id=$1", id, canOpenClasses, canCreateCourses); err != nil {
		return AccountView{}, "", err
	}
	if err := audit(ctx, tx, actor, "account.created", id); err != nil {
		return AccountView{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return AccountView{}, "", err
	}
	view, err := s.account(ctx, id)
	return view, password, err
}

// insertAccount opens an active account that must change its generated initial password at first sign-in.
// Accounts opened inside a demo belong to it: they see no real data and go away at demo reset.
func insertAccount(ctx context.Context, tx pgx.Tx, login, displayName, demoOwner string) (string, string, error) {
	normalized, err := NormalizeLogin(login)
	if err != nil {
		return "", "", err
	}
	name, err := validName(displayName, 64)
	if err != nil {
		return "", "", err
	}
	password := InitialPassword()
	hash, err := HashPassword(password)
	if err != nil {
		return "", "", err
	}
	id := platform.ID("acc")
	if _, err := tx.Exec(ctx, "INSERT INTO accounts(id,login,display_name,password_hash,is_demo,demo_account_id) VALUES($1,$2,$3,$4,$5<>'',NULLIF($5,''))",
		id, normalized, name, hash, demoOwner); err != nil {
		if isUniqueViolation(err) {
			return "", "", fmt.Errorf("%w: 登录名 %s 已被使用", ErrConflict, normalized)
		}
		return "", "", err
	}
	return id, password, nil
}

func (s *Service) account(ctx context.Context, id string) (AccountView, error) {
	return scanAccount(s.db.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=$1", id))
}

// Accounts lists every account an operator can see: all real ones, or only a demo's own.
func (s *Service) Accounts(ctx context.Context, actor Roles) ([]AccountView, error) {
	if !actor.Operator {
		return nil, ErrForbidden
	}
	rows, err := s.db.Query(ctx, "SELECT "+accountColumns+" FROM accounts WHERE password_hash<>'!' AND ($1='' OR demo_account_id=$1) ORDER BY login", actor.DemoOwner)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanAccount)
}

type AccountChange struct {
	CanOpenClasses   *bool   `json:"can_open_classes"`
	CanCreateCourses *bool   `json:"can_create_courses"`
	Status           *string `json:"status"`
}

// UpdateAccount is for operators: class and course permissions, and disabling, which ends every session.
func (s *Service) UpdateAccount(ctx context.Context, actor Roles, id string, change AccountChange) (AccountView, error) {
	if !actor.Operator {
		return AccountView{}, ErrForbidden
	}
	if change.Status != nil && *change.Status != "active" && *change.Status != "disabled" {
		return AccountView{}, fmt.Errorf("%w: 状态只能是 active 或 disabled", ErrRejected)
	}
	if id == actor.AccountID && change.Status != nil && *change.Status == "disabled" {
		return AccountView{}, fmt.Errorf("%w: 不能停用自己的账号", ErrRejected)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AccountView{}, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE accounts SET can_open_classes=COALESCE($2,can_open_classes),can_create_courses=COALESCE($3,can_create_courses),
		status=COALESCE($4,status),updated_at=now() WHERE id=$1 AND password_hash<>'!' AND ($5='' OR demo_account_id=$5)`,
		id, change.CanOpenClasses, change.CanCreateCourses, change.Status, actor.DemoOwner)
	if err != nil {
		return AccountView{}, err
	}
	if tag.RowsAffected() == 0 {
		return AccountView{}, ErrNotFound
	}
	if change.Status != nil && *change.Status == "disabled" {
		if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL", id); err != nil {
			return AccountView{}, err
		}
	}
	if err := audit(ctx, tx, actor, "account.updated", id); err != nil {
		return AccountView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AccountView{}, err
	}
	return s.account(ctx, id)
}

// ResetPassword gives an account a new initial password and ends its sessions. Operators may reset any account
// they can see except other operators; a teacher only the students of their own classes.
func (s *Service) ResetPassword(ctx context.Context, actor Roles, id string) (string, error) {
	var operator bool
	var owner string
	err := s.db.QueryRow(ctx, "SELECT is_operator,COALESCE(demo_account_id,'') FROM accounts WHERE id=$1 AND password_hash<>'!'", id).Scan(&operator, &owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && actor.DemoOwner != "" && owner != actor.DemoOwner) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	allowed := actor.Operator && !operator
	if !allowed {
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teaching_space_members t JOIN teaching_space_members st ON st.space_id=t.space_id
			WHERE t.account_id=$1 AND t.role='teacher' AND st.account_id=$2 AND st.role='student')`, actor.AccountID, id).Scan(&allowed); err != nil {
			return "", err
		}
	}
	if !allowed {
		return "", ErrForbidden
	}
	password := InitialPassword()
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "UPDATE accounts SET password_hash=$2,must_change_password=true,password_changed_at=now(),updated_at=now() WHERE id=$1", id, hash); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL", id); err != nil {
		return "", err
	}
	if err := audit(ctx, tx, actor, "account.password_reset", id); err != nil {
		return "", err
	}
	return password, tx.Commit(ctx)
}

// CreateClass opens a social class. A teacher allowed to open classes teaches it themselves;
// an operator names the teacher, who must be a non-operator account they can see.
func (s *Service) CreateClass(ctx context.Context, actor Roles, name, teacherID string) (ClassView, error) {
	switch {
	case actor.Operator:
		if teacherID == "" {
			return ClassView{}, fmt.Errorf("%w: 请选择任教老师", ErrRejected)
		}
		var ok bool
		if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM accounts WHERE id=$1 AND NOT is_operator AND password_hash<>'!' AND status='active' AND ($2='' OR demo_account_id=$2))",
			teacherID, actor.DemoOwner).Scan(&ok); err != nil {
			return ClassView{}, err
		}
		if !ok {
			return ClassView{}, fmt.Errorf("%w: 任教老师不存在或已停用", ErrRejected)
		}
	case actor.CanOpenClasses:
		if teacherID != "" && teacherID != actor.AccountID {
			return ClassView{}, ErrForbidden
		}
		teacherID = actor.AccountID
	default:
		return ClassView{}, ErrForbidden
	}
	name, err := validName(name, 100)
	if err != nil {
		return ClassView{}, err
	}
	class := ClassView{ID: platform.ID("cls"), Name: name, IsDemo: actor.DemoOwner != ""}
	if teacherID == actor.AccountID {
		class.Role = "teacher"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return class, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "INSERT INTO teaching_spaces(id,name,opened_by,demo_account_id) VALUES($1,$2,$3,NULLIF($4,''))", class.ID, name, actor.AccountID, actor.DemoOwner); err != nil {
		return class, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO teaching_space_members(space_id,account_id,role) VALUES($1,$2,'teacher')", class.ID, teacherID); err != nil {
		return class, err
	}
	if err := audit(ctx, tx, actor, "class.created", class.ID); err != nil {
		return class, err
	}
	return class, tx.Commit(ctx)
}

// Classes lists every social class for operators (inside the demo boundary), and the caller's own classes for everyone else.
func (s *Service) Classes(ctx context.Context, actor Roles) ([]ClassView, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id,c.name,c.demo_account_id IS NOT NULL,COALESCE(m.role,''),
		(SELECT count(*) FROM teaching_space_members st WHERE st.space_id=c.id AND st.role='student')
		FROM teaching_spaces c LEFT JOIN teaching_space_members m ON m.space_id=c.id AND m.account_id=$2
		WHERE c.status='active' AND (m.role IS NOT NULL OR ($1 AND c.school_id IS NULL AND ($3='' OR c.demo_account_id=$3)))
		ORDER BY c.created_at,c.id`, actor.Operator, actor.AccountID, actor.DemoOwner)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (ClassView, error) {
		var c ClassView
		err := row.Scan(&c.ID, &c.Name, &c.IsDemo, &c.Role, &c.Students)
		return c, err
	})
}

// manageClass checks the caller may manage a class's students: an operator who can see it, or one of its teachers.
func (s *Service) manageClass(ctx context.Context, actor Roles, classID string) (string, error) {
	var owner, role string
	err := s.db.QueryRow(ctx, `SELECT COALESCE(c.demo_account_id,''),COALESCE(m.role,'') FROM teaching_spaces c
		LEFT JOIN teaching_space_members m ON m.space_id=c.id AND m.account_id=$2 WHERE c.id=$1 AND c.status='active'`, classID, actor.AccountID).Scan(&owner, &role)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && actor.DemoOwner != "" && owner != actor.DemoOwner) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if role != "teacher" && !actor.Operator {
		return "", ErrForbidden
	}
	return owner, nil
}

func (s *Service) ClassMembers(ctx context.Context, actor Roles, classID string) ([]ClassMember, error) {
	if _, err := s.manageClass(ctx, actor, classID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+memberColumns+`,m.role
		FROM teaching_space_members m JOIN accounts a ON a.id=m.account_id WHERE m.space_id=$1 ORDER BY m.role DESC,a.login`, classID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (ClassMember, error) {
		var m ClassMember
		err := row.Scan(append(accountFields(&m.Account), &m.Role)...)
		return m, err
	})
}

// CreateStudents opens student accounts in a class, all or none: one taken login rejects the whole batch,
// and the error names it. Initial passwords appear only in this response.
func (s *Service) CreateStudents(ctx context.Context, actor Roles, classID string, students []NewStudent) ([]CreatedStudent, error) {
	if len(students) == 0 || len(students) > MaxStudentsPerRequest {
		return nil, fmt.Errorf("%w: 一次要开 1 到 %d 个学员账号", ErrRejected, MaxStudentsPerRequest)
	}
	owner, err := s.manageClass(ctx, actor, classID)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	created := make([]CreatedStudent, 0, len(students))
	for _, student := range students {
		id, password, err := insertAccount(ctx, tx, student.Login, student.DisplayName, owner)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO teaching_space_members(space_id,account_id,role) VALUES($1,$2,'student')", classID, id); err != nil {
			return nil, err
		}
		if err := audit(ctx, tx, actor, "account.created", id); err != nil {
			return nil, err
		}
		account, err := scanAccount(tx.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=$1", id))
		if err != nil {
			return nil, err
		}
		created = append(created, CreatedStudent{Member: ClassMember{Account: account, Role: "student"}, InitialPassword: password})
	}
	return created, tx.Commit(ctx)
}

// RemoveStudent takes a student out of a class; the account stays, and only operators disable accounts.
func (s *Service) RemoveStudent(ctx context.Context, actor Roles, classID, accountID string) error {
	if _, err := s.manageClass(ctx, actor, classID); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "DELETE FROM teaching_space_members WHERE space_id=$1 AND account_id=$2 AND role='student'", classID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := audit(ctx, tx, actor, "class.student_removed", classID+":"+accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func collect[T any](rows pgx.Rows, scan func(pgx.Row) (T, error)) ([]T, error) {
	defer rows.Close()
	result := []T{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
