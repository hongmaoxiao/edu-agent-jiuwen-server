package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"edu-agent-jiuwen-server/internal/platform"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type AccountView struct {
	ID                 string `json:"id"`
	Login              string `json:"login"`
	DisplayName        string `json:"display_name"`
	IsOperator         bool   `json:"is_operator"`
	CanCreateCourses   bool   `json:"can_create_courses"`
	CanOpenClasses     bool   `json:"can_open_classes"`
	IsDemo             bool   `json:"is_demo"`
	MustChangePassword bool   `json:"must_change_password"`
	Status             string `json:"status"`
}

type SchoolView struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	City   string   `json:"city"`
	IsDemo bool     `json:"is_demo"`
	Roles  []string `json:"roles,omitempty"`
}

// DemoView appears only in demo sessions: the frontend shows the perspective switcher from it.
type DemoView struct {
	Perspective  string   `json:"perspective"`
	Perspectives []string `json:"perspectives"`
	DisplayName  string   `json:"display_name"` // the demo account that signed in
}

type Me struct {
	Account AccountView  `json:"account"`
	Schools []SchoolView `json:"schools"`
	Classes []ClassView  `json:"classes"` // classes the caller teaches or learns in, with their role
	Demo    *DemoView    `json:"demo,omitempty"`
}

const (
	accountColumns = "id,login,display_name,is_operator,can_create_courses,can_open_classes,is_demo,must_change_password,status"
	// memberColumns is accountColumns for queries that join accounts as a.
	memberColumns = "a.id,a.login,a.display_name,a.is_operator,a.can_create_courses,a.can_open_classes,a.is_demo,a.must_change_password,a.status"
)

// accountFields are the scan targets for accountColumns, in the same order.
func accountFields(a *AccountView) []any {
	return []any{&a.ID, &a.Login, &a.DisplayName, &a.IsOperator, &a.CanCreateCourses, &a.CanOpenClasses, &a.IsDemo, &a.MustChangePassword, &a.Status}
}

func scanAccount(row pgx.Row) (AccountView, error) {
	var a AccountView
	err := row.Scan(accountFields(&a)...)
	return a, err
}

func (s *Service) Me(ctx context.Context, p Principal) (Me, error) {
	account, err := s.account(ctx, p.AccountID)
	if err != nil {
		return Me{}, err
	}
	// 首次登录改密码看的是登录账号本人。
	if p.LoginAccountID != p.AccountID {
		account.MustChangePassword = p.MustChangePassword
	}
	roles, err := s.Roles(ctx, p)
	if err != nil {
		return Me{}, err
	}
	me := Me{Account: account, Schools: []SchoolView{}}
	if me.Demo, err = s.demoView(ctx, p); err != nil {
		return me, err
	}
	// Operators see every class on the management pages; here only their own memberships count.
	if me.Classes, err = s.Classes(ctx, Roles{AccountID: p.AccountID}); err != nil {
		return me, err
	}
	rows, err := s.db.Query(ctx, "SELECT id,name,city,is_demo FROM schools WHERE id IN (SELECT school_id FROM school_members WHERE account_id=$1) ORDER BY name", p.AccountID)
	if err != nil {
		return me, err
	}
	defer rows.Close()
	for rows.Next() {
		var school SchoolView
		if err := rows.Scan(&school.ID, &school.Name, &school.City, &school.IsDemo); err != nil {
			return me, err
		}
		school.Roles = roles.Schools[school.ID]
		me.Schools = append(me.Schools, school)
	}
	return me, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func validName(value string, max int) (string, error) {
	name := strings.TrimSpace(value)
	if n := utf8.RuneCountInString(name); n < 1 || n > max {
		return "", fmt.Errorf("%w: 名称长度要在 1 到 %d 个字之间", ErrRejected, max)
	}
	return name, nil
}

func (s *Service) CreateSchool(ctx context.Context, actor Roles, name, city string) (SchoolView, error) {
	if !actor.Operator {
		return SchoolView{}, ErrForbidden
	}
	name, err := validName(name, 100)
	if err != nil {
		return SchoolView{}, err
	}
	school := SchoolView{ID: platform.ID("sch"), Name: name, City: strings.TrimSpace(city)}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return school, err
	}
	defer tx.Rollback(ctx)
	school.IsDemo = actor.DemoOwner != ""
	if _, err := tx.Exec(ctx, "INSERT INTO schools(id,name,city,is_demo,demo_account_id) VALUES($1,$2,$3,$4,NULLIF($5,''))", school.ID, school.Name, school.City, school.IsDemo, actor.DemoOwner); err != nil {
		if isUniqueViolation(err) {
			return school, fmt.Errorf("%w: 已有同名学校", ErrConflict)
		}
		return school, err
	}
	if err := audit(ctx, tx, actor, "school.created", school.ID); err != nil {
		return school, err
	}
	return school, tx.Commit(ctx)
}

// Schools lists every school for operators and the caller's own schools for everyone else.
// A demo operator's "every school" is only the schools of its own demo.
func (s *Service) Schools(ctx context.Context, actor Roles) ([]SchoolView, error) {
	query := `SELECT id,name,city,is_demo FROM schools WHERE status='active'
		AND (($1 AND ($3='' OR demo_account_id=$3)) OR id IN (SELECT school_id FROM school_members WHERE account_id=$2)) ORDER BY name`
	rows, err := s.db.Query(ctx, query, actor.Operator, actor.AccountID, actor.DemoOwner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SchoolView{}
	for rows.Next() {
		var school SchoolView
		if err := rows.Scan(&school.ID, &school.Name, &school.City, &school.IsDemo); err != nil {
			return nil, err
		}
		school.Roles = actor.Schools[school.ID]
		result = append(result, school)
	}
	return result, rows.Err()
}

type Member struct {
	Account AccountView `json:"account"`
	Roles   []string    `json:"roles"`
}

// school returns a school's demo owner ("" for a real school), or ErrNotFound when the school
// does not exist or lies outside the actor's demo. This is the demo boundary for school-scoped requests.
func (s *Service) school(ctx context.Context, actor Roles, schoolID string) (string, error) {
	var owner string
	err := s.db.QueryRow(ctx, "SELECT COALESCE(demo_account_id,'') FROM schools WHERE id=$1 AND status='active'", schoolID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && actor.DemoOwner != "" && owner != actor.DemoOwner) {
		return "", ErrNotFound
	}
	return owner, err
}

// CreateMember opens an account with one or more roles in a school, e.g. a teacher who is also the school admin.
func (s *Service) CreateMember(ctx context.Context, actor Roles, schoolID, login, displayName string, roles []string) (Member, string, error) {
	slices.Sort(roles)
	roles = slices.Compact(roles)
	if len(roles) == 0 {
		return Member{}, "", fmt.Errorf("%w: 至少要给一个角色", ErrRejected)
	}
	for _, role := range roles {
		if role != "school_admin" && role != "teacher" && role != "student" {
			return Member{}, "", fmt.Errorf("%w: 角色只能是 school_admin、teacher 或 student", ErrRejected)
		}
	}
	owner, err := s.school(ctx, actor, schoolID)
	if err != nil {
		return Member{}, "", err
	}
	for _, role := range roles {
		if !actor.CanGrant(schoolID, role) {
			return Member{}, "", ErrForbidden
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Member{}, "", err
	}
	defer tx.Rollback(ctx)
	// 在演示学校里开的账号也归这个演示所有：同样看不到真实数据，重置时一起删掉。
	id, password, err := insertAccount(ctx, tx, login, displayName, owner)
	if err != nil {
		return Member{}, "", err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO school_members(school_id,account_id,role) SELECT $1,$2,unnest($3::text[])", schoolID, id, roles); err != nil {
		return Member{}, "", err
	}
	if err := audit(ctx, tx, actor, "account.created", id); err != nil {
		return Member{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, "", err
	}
	account, err := s.account(ctx, id)
	return Member{Account: account, Roles: roles}, password, err
}

func (s *Service) Members(ctx context.Context, actor Roles, schoolID string) ([]Member, error) {
	if _, err := s.school(ctx, actor, schoolID); err != nil {
		return nil, err
	}
	if !actor.ManagesSchool(schoolID) {
		return nil, ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT `+memberColumns+`,array_agg(m.role ORDER BY m.role)
		FROM school_members m JOIN accounts a ON a.id=m.account_id WHERE m.school_id=$1 GROUP BY a.id ORDER BY a.login`, schoolID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (Member, error) {
		var m Member
		err := row.Scan(append(accountFields(&m.Account), &m.Roles)...)
		return m, err
	})
}

// CreateOperator is for cmd/admin: the first operator chooses their own password, so no forced change.
func (s *Service) CreateOperator(ctx context.Context, login, displayName, password string) (AccountView, error) {
	normalized, err := NormalizeLogin(login)
	if err != nil {
		return AccountView{}, err
	}
	name, err := validName(displayName, 64)
	if err != nil {
		return AccountView{}, err
	}
	if err := ValidPassword(password); err != nil {
		return AccountView{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return AccountView{}, err
	}
	id := platform.ID("acc")
	if _, err := s.db.Exec(ctx, "INSERT INTO accounts(id,login,display_name,password_hash,must_change_password,is_operator,can_create_courses) VALUES($1,$2,$3,$4,false,true,true)", id, normalized, name, hash); err != nil {
		if isUniqueViolation(err) {
			return AccountView{}, fmt.Errorf("%w: 登录名已被使用", ErrConflict)
		}
		return AccountView{}, err
	}
	return s.account(ctx, id)
}

// SetPassword is for cmd/admin: the operator chose this password, so no forced change; every session of the account ends.
func (s *Service) SetPassword(ctx context.Context, login, password string) error {
	normalized, err := NormalizeLogin(login)
	if err != nil {
		return err
	}
	if err := ValidPassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, "UPDATE accounts SET password_hash=$2,must_change_password=false,password_changed_at=now(),updated_at=now() WHERE login=$1 AND password_hash<>'!' RETURNING id", normalized, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL", id); err != nil {
		return err
	}
	if err := audit(ctx, tx, Roles{AccountID: id}, "account.password_set_by_admin", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
