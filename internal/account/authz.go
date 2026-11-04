package account

import (
	"context"
	"slices"
)

// Roles is what one account may do. Every permission check in the server goes through here.
type Roles struct {
	AccountID        string
	Operator         bool
	CanCreateCourses bool
	Schools          map[string][]string // school id → school_admin / teacher / student
}

func (s *Service) Roles(ctx context.Context, accountID string) (Roles, error) {
	r := Roles{AccountID: accountID, Schools: map[string][]string{}}
	if err := s.db.QueryRow(ctx, "SELECT is_operator,can_create_courses FROM accounts WHERE id=$1", accountID).Scan(&r.Operator, &r.CanCreateCourses); err != nil {
		return r, err
	}
	rows, err := s.db.Query(ctx, "SELECT school_id,role FROM school_members WHERE account_id=$1 ORDER BY school_id,role", accountID)
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

// ManagesSchool: operators manage every school, school admins their own.
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
