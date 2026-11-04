package account

import (
	"context"
	"errors"
	"fmt"
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

type Me struct {
	Account AccountView  `json:"account"`
	Schools []SchoolView `json:"schools"`
}

const accountColumns = "id,login,display_name,is_operator,can_create_courses,is_demo,must_change_password,status"

func scanAccount(row pgx.Row) (AccountView, error) {
	var a AccountView
	err := row.Scan(&a.ID, &a.Login, &a.DisplayName, &a.IsOperator, &a.CanCreateCourses, &a.IsDemo, &a.MustChangePassword, &a.Status)
	return a, err
}

func (s *Service) Me(ctx context.Context, accountID string) (Me, error) {
	account, err := scanAccount(s.db.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=$1", accountID))
	if err != nil {
		return Me{}, err
	}
	roles, err := s.Roles(ctx, accountID)
	if err != nil {
		return Me{}, err
	}
	me := Me{Account: account, Schools: []SchoolView{}}
	rows, err := s.db.Query(ctx, "SELECT id,name,city,is_demo FROM schools WHERE id IN (SELECT school_id FROM school_members WHERE account_id=$1) ORDER BY name", accountID)
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
	if _, err := tx.Exec(ctx, "INSERT INTO schools(id,name,city) VALUES($1,$2,$3)", school.ID, school.Name, school.City); err != nil {
		if isUniqueViolation(err) {
			return school, fmt.Errorf("%w: 已有同名学校", ErrConflict)
		}
		return school, err
	}
	if err := audit(ctx, tx, actor.AccountID, "school.created", school.ID); err != nil {
		return school, err
	}
	return school, tx.Commit(ctx)
}

// Schools lists every school for operators and the caller's own schools for everyone else.
func (s *Service) Schools(ctx context.Context, actor Roles) ([]SchoolView, error) {
	query := "SELECT id,name,city,is_demo FROM schools WHERE status='active' AND ($1 OR id IN (SELECT school_id FROM school_members WHERE account_id=$2)) ORDER BY name"
	rows, err := s.db.Query(ctx, query, actor.Operator, actor.AccountID)
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

func (s *Service) schoolExists(ctx context.Context, schoolID string) error {
	var exists bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schools WHERE id=$1 AND status='active')", schoolID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// CreateMember opens a new account inside a school. The initial password is returned once and must be changed at first sign-in.
func (s *Service) CreateMember(ctx context.Context, actor Roles, schoolID, login, displayName, role string) (Member, string, error) {
	if !actor.CanGrant(schoolID, role) {
		if role != "school_admin" && role != "teacher" && role != "student" {
			return Member{}, "", fmt.Errorf("%w: 角色只能是 school_admin、teacher 或 student", ErrRejected)
		}
		return Member{}, "", ErrForbidden
	}
	if err := s.schoolExists(ctx, schoolID); err != nil {
		return Member{}, "", err
	}
	normalized, err := NormalizeLogin(login)
	if err != nil {
		return Member{}, "", err
	}
	name, err := validName(displayName, 64)
	if err != nil {
		return Member{}, "", err
	}
	password := InitialPassword()
	hash, err := HashPassword(password)
	if err != nil {
		return Member{}, "", err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Member{}, "", err
	}
	defer tx.Rollback(ctx)
	id := platform.ID("acc")
	if _, err := tx.Exec(ctx, "INSERT INTO accounts(id,login,display_name,password_hash) VALUES($1,$2,$3,$4)", id, normalized, name, hash); err != nil {
		if isUniqueViolation(err) {
			return Member{}, "", fmt.Errorf("%w: 登录名已被使用", ErrConflict)
		}
		return Member{}, "", err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO school_members(school_id,account_id,role) VALUES($1,$2,$3)", schoolID, id, role); err != nil {
		return Member{}, "", err
	}
	if err := audit(ctx, tx, actor.AccountID, "account.created", id); err != nil {
		return Member{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, "", err
	}
	account, err := scanAccount(s.db.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=$1", id))
	return Member{Account: account, Roles: []string{role}}, password, err
}

func (s *Service) Members(ctx context.Context, actor Roles, schoolID string) ([]Member, error) {
	if !actor.ManagesSchool(schoolID) {
		return nil, ErrForbidden
	}
	if err := s.schoolExists(ctx, schoolID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT a.id,a.login,a.display_name,a.is_operator,a.can_create_courses,a.is_demo,a.must_change_password,a.status,array_agg(m.role ORDER BY m.role)
		FROM school_members m JOIN accounts a ON a.id=m.account_id WHERE m.school_id=$1 GROUP BY a.id ORDER BY a.login`, schoolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Member{}
	for rows.Next() {
		var m Member
		a := &m.Account
		if err := rows.Scan(&a.ID, &a.Login, &a.DisplayName, &a.IsOperator, &a.CanCreateCourses, &a.IsDemo, &a.MustChangePassword, &a.Status, &m.Roles); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
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
	return scanAccount(s.db.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=$1", id))
}
