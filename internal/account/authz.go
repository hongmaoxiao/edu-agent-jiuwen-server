package account

import (
	"context"
	"slices"
)

// Roles is what one request may do. Every permission check in the server goes through here.
type Roles struct {
	AccountID        string // who the request acts as (the persona in a demo session)
	LoginAccountID   string // who signed in
	Operator         bool
	CanCreateCourses bool
	CanOpenClasses   bool
	// DemoOwner is set for demo personas and for accounts opened inside a demo: such an account
	// only ever sees schools owned by the same demo account, so a demo can never reach real data.
	DemoOwner string
	Schools   map[string][]string // school id → school_admin / teacher / student
}

func (s *Service) Roles(ctx context.Context, p Principal) (Roles, error) {
	r := Roles{AccountID: p.AccountID, LoginAccountID: p.LoginAccountID, Schools: map[string][]string{}}
	if err := s.db.QueryRow(ctx, "SELECT is_operator,can_create_courses,can_open_classes,COALESCE(demo_account_id,'') FROM accounts WHERE id=$1", p.AccountID).
		Scan(&r.Operator, &r.CanCreateCourses, &r.CanOpenClasses, &r.DemoOwner); err != nil {
		return r, err
	}
	rows, err := s.db.Query(ctx, "SELECT school_id,role FROM school_members WHERE account_id=$1 ORDER BY school_id,role", p.AccountID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var school, role string
		if err := rows.Scan(&school, &role); err != nil {
			return r, err
		}
		r.Schools[school] = append(r.Schools[school], role)
	}
	return r, rows.Err()
}

func (r Roles) Has(schoolID, role string) bool { return slices.Contains(r.Schools[schoolID], role) }

// ManagesSchool: operators manage every school they can see, school admins their own.
// Callers must first load the school with Service.school, which applies the demo boundary.
func (r Roles) ManagesSchool(schoolID string) bool {
	return r.Operator || r.Has(schoolID, "school_admin")
}

// CanGrant: only operators appoint school admins; school admins open teacher and student accounts.
func (r Roles) CanGrant(schoolID, role string) bool {
	switch role {
	case "school_admin":
		return r.Operator
	case "teacher", "student":
		return r.ManagesSchool(schoolID)
	}
	return false
}
