// Package teaching serves the numbers and the graded work behind 管理 · 总览/用量/院校/模型/预算,
// 教学 · 作品评阅/成绩册/申诉 and 成绩 · 我的作品/用量. Every query applies the demo boundary: a row is
// visible only when its demo owner is the caller's (none for real accounts), so demo data never reaches
// real accounts and a demo never sees real data.
package teaching

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"edu-agent-jiuwen-server/internal/account"
)

type Service struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

var beijing = time.FixedZone("CST", 8*3600)

func monthStart(now time.Time) time.Time {
	d := today(now)
	return d.AddDate(0, 0, 1-d.Day())
}

// today is the current day in Beijing, as the usage rows record it.
func today(now time.Time) time.Time {
	y, m, d := now.In(beijing).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Scope picks which usage a page shows: the whole platform, one school, the social classes, the caller's
// classes as a teacher (optionally one), or the caller's own.
type Scope struct {
	Kind     string // platform | school | social | teach | mine
	SchoolID string
	ClassID  string
}

// filter turns a scope into a WHERE clause over usage_records u (and teaching_spaces c for class counts), after
// checking the caller may see it.
type filter struct {
	usage   string // over usage_records u
	classes string // over teaching_spaces c
	args    []any
	label   string
}

func (s *Service) filter(ctx context.Context, r account.Roles, sc Scope) (filter, error) {
	owner := "IS NOT DISTINCT FROM NULLIF($1,'')"
	f := filter{args: []any{r.DemoOwner}}
	switch sc.Kind {
	case "platform", "social":
		if !r.Operator {
			return f, account.ErrForbidden
		}
		f.usage, f.classes, f.label = "u.demo_account_id "+owner, "c.demo_account_id "+owner, "全平台"
		if sc.Kind == "social" {
			f.usage += " AND u.school_id IS NULL"
			f.classes += " AND c.school_id IS NULL"
			f.label = "社会班"
		}
	case "school":
		var name, schoolOwner string
		err := s.db.QueryRow(ctx, "SELECT name,COALESCE(demo_account_id,'') FROM schools WHERE id=$1 AND status='active'", sc.SchoolID).Scan(&name, &schoolOwner)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && schoolOwner != r.DemoOwner) {
			return f, account.ErrNotFound
		}
		if err != nil {
			return f, err
		}
		if !r.ManagesSchool(sc.SchoolID) {
			return f, account.ErrForbidden
		}
		f.args = append(f.args, sc.SchoolID)
		f.usage, f.classes, f.label = "u.demo_account_id "+owner+" AND u.school_id=$2", "c.demo_account_id "+owner+" AND c.school_id=$2", name
	case "teach":
		f.args = []any{r.AccountID}
		taught := "(SELECT space_id FROM teaching_space_members WHERE account_id=$1 AND role='teacher')"
		f.usage, f.classes, f.label = "u.class_id IN "+taught, "c.id IN "+taught, "我教的班"
		if sc.ClassID != "" {
			f.args = append(f.args, sc.ClassID)
			f.usage += " AND u.class_id=$2"
			f.classes += " AND c.id=$2"
		}
	case "mine":
		f.args = []any{r.AccountID}
		f.usage, f.classes, f.label = "u.account_id=$1", "c.id IN (SELECT space_id FROM teaching_space_members WHERE account_id=$1)", "我的用量"
	default:
		return f, fmt.Errorf("%w: 范围只能是 platform、school、social、teach 或 mine", account.ErrRejected)
	}
	return f, nil
}

type Point struct {
	Day    string `json:"day"`
	Tokens int64  `json:"tokens"`
	Calls  int64  `json:"calls"`
}

type Share struct {
	Name   string  `json:"name"`
	Tokens int64   `json:"tokens"`
	Calls  int64   `json:"calls"`
	Cost   float64 `json:"cost"`
}

type RankRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // school | class
	Tokens   int64  `json:"tokens"`
	Students int64  `json:"students"`
	Calls    int64  `json:"calls"`
}

type Overview struct {
	Label         string    `json:"label"`
	Today         int64     `json:"today_tokens"`
	Yesterday     int64     `json:"yesterday_tokens"`
	Month         int64     `json:"month_tokens"`
	Total         int64     `json:"total_tokens"`
	MonthCalls    int64     `json:"month_calls"`
	Students      int64     `json:"students"`
	Schools       int64     `json:"schools"`
	Classes       int64     `json:"classes"`
	SocialClasses int64     `json:"social_classes"`
	Trend         []Point   `json:"trend"`
	Models        []Share   `json:"models"`
	Rank          []RankRow `json:"rank"`
	AsOf          time.Time `json:"as_of"`
}

func (s *Service) Overview(ctx context.Context, r account.Roles, sc Scope, now time.Time) (Overview, error) {
	f, err := s.filter(ctx, r, sc)
	if err != nil {
		return Overview{}, err
	}
	day := today(now)
	month := monthStart(now)
	o := Overview{Label: f.label, AsOf: now}
	n := len(f.args)
	args := append(f.args, day, day.AddDate(0, 0, -1), month)
	if err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT
		COALESCE(SUM(input_tokens+output_tokens) FILTER (WHERE day=$%[1]d),0),
		COALESCE(SUM(input_tokens+output_tokens) FILTER (WHERE day=$%[2]d),0),
		COALESCE(SUM(input_tokens+output_tokens) FILTER (WHERE day>=$%[3]d),0),
		COALESCE(SUM(calls) FILTER (WHERE day>=$%[3]d),0),
		COALESCE(SUM(input_tokens+output_tokens),0)
		FROM usage_records u WHERE %s AND day<=$%[1]d`, n+1, n+2, n+3, f.usage), args...).Scan(&o.Today, &o.Yesterday, &o.Month, &o.MonthCalls, &o.Total); err != nil {
		return o, err
	}
	if err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(DISTINCT m.account_id),COUNT(DISTINCT c.id),
		COUNT(DISTINCT c.id) FILTER (WHERE c.school_id IS NULL),COUNT(DISTINCT c.school_id)
		FROM teaching_spaces c LEFT JOIN teaching_space_members m ON m.space_id=c.id AND m.role='student'
		WHERE c.status='active' AND %s`, f.classes), f.args...).Scan(&o.Students, &o.Classes, &o.SocialClasses, &o.Schools); err != nil {
		return o, err
	}
	if sc.Kind == "platform" {
		// Schools without classes still count as connected.
		if err := s.db.QueryRow(ctx, "SELECT COUNT(*) FROM schools WHERE status='active' AND demo_account_id IS NOT DISTINCT FROM NULLIF($1,'')", r.DemoOwner).Scan(&o.Schools); err != nil {
			return o, err
		}
	}
	if o.Trend, err = s.trend(ctx, f, day.AddDate(0, 0, -29), day); err != nil {
		return o, err
	}
	if o.Models, err = s.byModel(ctx, f, month, day); err != nil {
		return o, err
	}
	o.Rank, err = s.rank(ctx, f, sc, month, day)
	return o, err
}

func (s *Service) trend(ctx context.Context, f filter, from, to time.Time) ([]Point, error) {
	n := len(f.args)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT d::date,COALESCE(SUM(u.input_tokens+u.output_tokens),0),COALESCE(SUM(u.calls),0)
		FROM generate_series($%[1]d::date,$%[2]d::date,'1 day') d
		LEFT JOIN usage_records u ON u.day=d::date AND %s
		GROUP BY d ORDER BY d`, n+1, n+2, f.usage), append(f.args, from, to)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Point, error) {
		var p Point
		var d time.Time
		err := row.Scan(&d, &p.Tokens, &p.Calls)
		p.Day = d.Format(time.DateOnly)
		return p, err
	})
}

func (s *Service) byModel(ctx context.Context, f filter, from, to time.Time) ([]Share, error) {
	n := len(f.args)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT COALESCE(m.name,'其他'),SUM(u.input_tokens+u.output_tokens),SUM(u.calls),SUM(u.cost)::float8
		FROM usage_records u LEFT JOIN models m ON m.id=u.model_id
		WHERE %s AND u.day BETWEEN $%d AND $%d GROUP BY 1 ORDER BY 2 DESC`, f.usage, n+1, n+2), append(f.args, from, to)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Share])
}

// rank: the platform ranks schools and social classes together; a school ranks its classes.
func (s *Service) rank(ctx context.Context, f filter, sc Scope, from, to time.Time) ([]RankRow, error) {
	n := len(f.args)
	key, name, kind, join := "u.class_id", "c.name", "'class'", "JOIN teaching_spaces c ON c.id=u.class_id"
	if sc.Kind == "platform" {
		key, name, kind = "COALESCE(u.school_id,u.class_id)", "COALESCE(sch.name,c.name)", "CASE WHEN u.school_id IS NULL THEN 'class' ELSE 'school' END"
		join = "LEFT JOIN schools sch ON sch.id=u.school_id LEFT JOIN teaching_spaces c ON c.id=u.class_id AND u.school_id IS NULL"
	}
	rows, err := s.db.Query(ctx, fmt.Sprintf(`WITH r AS (
		SELECT %[1]s AS id,MIN(%[2]s) AS name,MIN(%[3]s) AS kind,SUM(u.input_tokens+u.output_tokens) AS tokens,SUM(u.calls) AS calls
		FROM usage_records u %[4]s
		WHERE %[5]s AND u.day BETWEEN $%[6]d AND $%[7]d AND %[1]s IS NOT NULL GROUP BY 1)
		SELECT r.id,r.name,r.kind,r.tokens,
		(SELECT COUNT(DISTINCT m.account_id) FROM teaching_space_members m JOIN teaching_spaces c2 ON c2.id=m.space_id
			WHERE m.role='student' AND (c2.id=r.id OR c2.school_id=r.id)),
		r.calls FROM r ORDER BY r.tokens DESC LIMIT 20`, key, name, kind, join, f.usage, n+1, n+2), append(f.args, from, to)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[RankRow])
}

// UsageRow is one group in the usage page's table.
type UsageRow struct {
	Key    string  `json:"key"`
	Name   string  `json:"name"`
	Extra  string  `json:"extra"` // school of a class, class of a student…
	Input  int64   `json:"input_tokens"`
	Output int64   `json:"output_tokens"`
	Calls  int64   `json:"calls"`
	Cost   float64 `json:"cost"`
}

type Usage struct {
	Label string     `json:"label"`
	From  string     `json:"from"`
	To    string     `json:"to"`
	Trend []Point    `json:"trend"`
	Rows  []UsageRow `json:"rows"`
	Total UsageRow   `json:"total"`
}

var groups = map[string][3]string{
	// key, name, extra
	"school":  {"u.school_id", "COALESCE(sch.name,'社会班')", "COALESCE(sch.city,'')"},
	"class":   {"u.class_id", "COALESCE(c.name,'（历史数据）')", "COALESCE(sch.name,'社会班')"},
	"lab":     {"u.lab_id", "COALESCE(l.title,'（历史数据）')", "''"},
	"model":   {"u.model_id", "COALESCE(m.name,'其他')", "COALESCE(m.provider,'')"},
	"student": {"u.account_id", "COALESCE(a.display_name,'（历史数据）')", "COALESCE(c.name,'')"},
	"day":     {"u.day::text", "u.day::text", "''"},
}

func (s *Service) Usage(ctx context.Context, r account.Roles, sc Scope, group string, days int, now time.Time) (Usage, error) {
	g, ok := groups[group]
	if !ok {
		return Usage{}, fmt.Errorf("%w: 分组只能是 school、class、lab、model、student 或 day", account.ErrRejected)
	}
	if days < 1 || days > 366 {
		return Usage{}, fmt.Errorf("%w: 天数要在 1 到 366 之间", account.ErrRejected)
	}
	f, err := s.filter(ctx, r, sc)
	if err != nil {
		return Usage{}, err
	}
	to := today(now)
	from := to.AddDate(0, 0, -(days - 1))
	u := Usage{Label: f.label, From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)}
	if u.Trend, err = s.trend(ctx, f, from, to); err != nil {
		return u, err
	}
	n := len(f.args)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT COALESCE(%[1]s,''),MIN(%[2]s),MIN(%[3]s),SUM(u.input_tokens),SUM(u.output_tokens),SUM(u.calls),SUM(u.cost)::float8
		FROM usage_records u
		LEFT JOIN schools sch ON sch.id=u.school_id LEFT JOIN teaching_spaces c ON c.id=u.class_id
		LEFT JOIN labs l ON l.id=u.lab_id LEFT JOIN models m ON m.id=u.model_id LEFT JOIN accounts a ON a.id=u.account_id
		WHERE %[4]s AND u.day BETWEEN $%[5]d AND $%[6]d
		GROUP BY 1 ORDER BY SUM(u.input_tokens)+SUM(u.output_tokens) DESC, 1 LIMIT 500`, g[0], g[1], g[2], f.usage, n+1, n+2), append(f.args, from, to)...)
	if err != nil {
		return u, err
	}
	if u.Rows, err = pgx.CollectRows(rows, pgx.RowToStructByPos[UsageRow]); err != nil {
		return u, err
	}
	if group == "day" {
		slices.Reverse(u.Rows) // newest day first reads better in a table; the chart already shows the order
	}
	u.Total.Name = "合计"
	for _, row := range u.Rows {
		u.Total.Input += row.Input
		u.Total.Output += row.Output
		u.Total.Calls += row.Calls
		u.Total.Cost += row.Cost
	}
	return u, nil
}

// SchoolStat is one row of 管理 · 院校.
type SchoolStat struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	City      string `json:"city"`
	Kind      string `json:"kind"`
	Contact   string `json:"contact"`
	Classes   int64  `json:"classes"`
	Teachers  int64  `json:"teachers"`
	Students  int64  `json:"students"`
	Month     int64  `json:"month_tokens"`
	Quota     *int64 `json:"monthly_quota"`
	CreatedAt string `json:"created_at"`
}

func (s *Service) Schools(ctx context.Context, r account.Roles, now time.Time) ([]SchoolStat, error) {
	if !r.Operator {
		return nil, account.ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT s.id,s.name,s.city,s.kind,s.contact,
		(SELECT COUNT(*) FROM teaching_spaces c WHERE c.school_id=s.id AND c.status='active'),
		(SELECT COUNT(DISTINCT account_id) FROM school_members WHERE school_id=s.id AND role='teacher'),
		(SELECT COUNT(DISTINCT account_id) FROM school_members WHERE school_id=s.id AND role='student'),
		(SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM usage_records u WHERE u.school_id=s.id AND u.day>=$2),
		(SELECT monthly_tokens FROM budgets b WHERE b.school_id=s.id),
		to_char(s.created_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD')
		FROM schools s WHERE s.status='active' AND s.demo_account_id IS NOT DISTINCT FROM NULLIF($1,'')
		ORDER BY 9 DESC, s.name`, r.DemoOwner, monthStart(now))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SchoolStat])
}

type Model struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Provider    string  `json:"provider"`
	Description string  `json:"description"`
	Enabled     bool    `json:"enabled"`
	InputPrice  float64 `json:"input_price"`
	OutputPrice float64 `json:"output_price"`
	Month       int64   `json:"month_tokens"`
	Calls       int64   `json:"month_calls"`
	Cost        float64 `json:"month_cost"`
}

func (s *Service) Models(ctx context.Context, r account.Roles, now time.Time) ([]Model, error) {
	if !r.Operator {
		return nil, account.ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT m.id,m.name,m.provider,m.description,m.enabled,m.input_price::float8,m.output_price::float8,
		COALESCE(SUM(u.input_tokens+u.output_tokens),0),COALESCE(SUM(u.calls),0),COALESCE(SUM(u.cost),0)::float8
		FROM models m LEFT JOIN usage_records u ON u.model_id=m.id AND u.day>=$2
		WHERE m.demo_account_id IS NOT DISTINCT FROM NULLIF($1,'')
		GROUP BY m.id ORDER BY m.position,m.name`, r.DemoOwner, monthStart(now))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Model])
}

type Budget struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // school | class
	Monthly int64  `json:"monthly_tokens"`
	Used    int64  `json:"used_tokens"`
}

// Budgets: operators see every school and social class; a school admin sees the own school.
func (s *Service) Budgets(ctx context.Context, r account.Roles, now time.Time) ([]Budget, error) {
	var schools []string
	for id := range r.Schools {
		if r.Has(id, "school_admin") {
			schools = append(schools, id)
		}
	}
	if !r.Operator && len(schools) == 0 {
		return nil, account.ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT b.id,COALESCE(sch.name,c.name),CASE WHEN b.school_id IS NULL THEN 'class' ELSE 'school' END,b.monthly_tokens,
		(SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM usage_records u
			WHERE u.day>=$3 AND (u.school_id=b.school_id OR (b.class_id IS NOT NULL AND u.class_id=b.class_id)))
		FROM budgets b LEFT JOIN schools sch ON sch.id=b.school_id LEFT JOIN teaching_spaces c ON c.id=b.class_id
		WHERE b.demo_account_id IS NOT DISTINCT FROM NULLIF($1,'') AND ($2 OR b.school_id=ANY($4))
		ORDER BY 5 DESC`, r.DemoOwner, r.Operator, monthStart(now), schools)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Budget])
}
