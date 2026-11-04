package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"edu-agent-jiuwen-server/internal/platform"

	"github.com/jackc/pgx/v5"
)

// Demo accounts let one person show a customer the operator, teacher and student views without signing out
// (docs/plan/01-information-architecture.md §5). Each perspective is a fixed persona inside the demo's own school.

var Perspectives = []string{"operator", "teacher", "student"}

func (s *Service) demoView(ctx context.Context, p Principal) (*DemoView, error) {
	if p.Perspective == "" {
		return nil, nil
	}
	view := &DemoView{Perspective: p.Perspective, Perspectives: Perspectives}
	if err := s.db.QueryRow(ctx, "SELECT display_name FROM accounts WHERE id=$1", p.LoginAccountID).Scan(&view.DisplayName); err != nil {
		return nil, err
	}
	return view, nil
}

// SwitchPerspective points a demo session at another persona and issues a new access token; the old one stops working.
// Only demo accounts may switch, and only to their own personas.
func (s *Service) SwitchPerspective(ctx context.Context, p Principal, perspective string) (Tokens, error) {
	if !slices.Contains(Perspectives, perspective) {
		return Tokens{}, fmt.Errorf("%w: 视角只能是 operator、teacher 或 student", ErrRejected)
	}
	var persona string
	err := s.db.QueryRow(ctx, "SELECT persona_account_id FROM demo_personas WHERE demo_account_id=$1 AND perspective=$2", p.LoginAccountID, perspective).Scan(&persona)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, ErrForbidden
	}
	if err != nil {
		return Tokens{}, err
	}
	tokens := Tokens{AccessToken: token("eat"), ExpiresIn: int(AccessTTL.Seconds())}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Tokens{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "UPDATE sessions SET acting_account_id=$1,access_hash=$2,access_expires_at=$3 WHERE id=$4", persona, Hash(tokens.AccessToken), time.Now().Add(AccessTTL), p.SessionID); err != nil {
		return Tokens{}, err
	}
	if err := audit(ctx, tx, Roles{AccountID: persona, LoginAccountID: p.LoginAccountID}, "demo.perspective."+perspective, p.SessionID); err != nil {
		return Tokens{}, err
	}
	return tokens, tx.Commit(ctx)
}

// CreateDemo is for cmd/admin: a demo account, its demo school and the three personas.
// Personas cannot sign in by themselves; their password hash never matches.
func (s *Service) CreateDemo(ctx context.Context, login, displayName, password string) (AccountView, error) {
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
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return AccountView{}, err
	}
	defer tx.Rollback(ctx)
	id := platform.ID("acc")
	if _, err := tx.Exec(ctx, "INSERT INTO accounts(id,login,display_name,password_hash,must_change_password,is_demo) VALUES($1,$2,$3,$4,false,true)", id, normalized, name, hash); err != nil {
		if isUniqueViolation(err) {
			return AccountView{}, fmt.Errorf("%w: 登录名已被使用", ErrConflict)
		}
		return AccountView{}, err
	}
	school := platform.ID("sch")
	if _, err := tx.Exec(ctx, "INSERT INTO schools(id,name,is_demo,demo_account_id) VALUES($1,$2,true,$3)", school, "演示学校（"+name+"）", id); err != nil {
		if isUniqueViolation(err) {
			return AccountView{}, fmt.Errorf("%w: 已有同名的演示学校", ErrConflict)
		}
		return AccountView{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO demo_setups(account_id,school_id) VALUES($1,$2)", id, school); err != nil {
		return AccountView{}, err
	}
	if err := createPersonas(ctx, tx, id, normalized, school); err != nil {
		return AccountView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AccountView{}, err
	}
	return scanAccount(s.db.QueryRow(ctx, "SELECT "+accountColumns+" FROM accounts WHERE id=$1", id))
}

func createPersonas(ctx context.Context, tx pgx.Tx, demo, login, school string) error {
	personas := []struct{ perspective, name, role string }{
		{"operator", "演示运营", ""},
		{"teacher", "演示教师", "teacher"},
		{"student", "演示学生", "student"},
	}
	for _, p := range personas {
		id := platform.ID("acc")
		if _, err := tx.Exec(ctx, `INSERT INTO accounts(id,login,display_name,password_hash,must_change_password,is_demo,demo_account_id,is_operator,can_create_courses)
			VALUES($1,$2,$3,'!',false,true,$4,$5,$6)`, id, limit(login, 50)+"."+p.perspective, p.name, demo, p.perspective == "operator", p.perspective != "student"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO demo_personas(demo_account_id,perspective,persona_account_id) VALUES($1,$2,$3)", demo, p.perspective, id); err != nil {
			return err
		}
		if p.role != "" {
			if _, err := tx.Exec(ctx, "INSERT INTO school_members(school_id,account_id,role) VALUES($1,$2,$3)", school, id, p.role); err != nil {
				return err
			}
		}
	}
	return nil
}

// ResetDemo puts a demo back to its initial state: everything opened during demos goes, the demo school and
// the three personas stay. Real schools and accounts are never touched; they are not owned by a demo account.
func (s *Service) ResetDemo(ctx context.Context, login string) error {
	normalized, err := NormalizeLogin(login)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var demo, school string
	err = tx.QueryRow(ctx, "SELECT d.account_id,d.school_id FROM demo_setups d JOIN accounts a ON a.id=d.account_id WHERE a.login=$1 FOR UPDATE OF d", normalized).Scan(&demo, &school)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	steps := []struct {
		sql  string
		args []any
	}{
		{"DELETE FROM accounts WHERE demo_account_id=$1 AND id NOT IN (SELECT persona_account_id FROM demo_personas WHERE demo_account_id=$1)", []any{demo}},
		{"DELETE FROM schools WHERE demo_account_id=$1 AND id<>$2", []any{demo, school}},
		{"DELETE FROM school_members WHERE school_id=$1", []any{school}},
		{`INSERT INTO school_members(school_id,account_id,role)
			SELECT $1,persona_account_id,perspective FROM demo_personas WHERE demo_account_id=$2 AND perspective IN ('teacher','student')`, []any{school, demo}},
	}
	for _, step := range steps {
		if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
			return err
		}
	}
	if err := audit(ctx, tx, Roles{AccountID: demo}, "demo.reset", school); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
