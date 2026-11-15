package teaching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"edu-agent-jiuwen-server/internal/account"
	"edu-agent-jiuwen-server/internal/platform"
)

// AppealWindow: a published grade can be appealed for this long.
const AppealWindow = 7 * 24 * time.Hour

// Status as people see it: reviewing (AI score only), draft (teacher started), published, appealing.
func status(raw string, appealOpen bool) string {
	switch {
	case raw == "published" && appealOpen:
		return "appealing"
	case raw == "published":
		return "published"
	case raw == "draft":
		return "draft"
	}
	return "pending"
}

type WorkRow struct {
	ID           string     `json:"id"`
	LabID        string     `json:"lab_id"`
	LabTitle     string     `json:"lab_title"`
	Chapter      string     `json:"chapter"`
	ClassID      string     `json:"class_id"`
	ClassName    string     `json:"class_name"`
	Student      string     `json:"student"`
	SubmittedAt  time.Time  `json:"submitted_at"`
	AITotal      float64    `json:"ai_total"`
	TeacherTotal *float64   `json:"teacher_total"`
	Status       string     `json:"status"`
	Flags        []string   `json:"flags"`
	PublishedAt  *time.Time `json:"published_at"`
}

type Appeal struct {
	ID         string     `json:"id"`
	Item       string     `json:"item"`
	Reason     string     `json:"reason"`
	Status     string     `json:"status"`
	Reply      string     `json:"reply"`
	CreatedAt  time.Time  `json:"created_at"`
	AnsweredAt *time.Time `json:"answered_at"`
}

type Work struct {
	WorkRow
	Files         []account.File `json:"files"`
	Steps         []account.Step `json:"steps"`
	OneShot       bool           `json:"one_shot"`
	Items         []account.Item `json:"items"`
	TeacherScores []int          `json:"teacher_scores"`
	Comment       string         `json:"comment"`
	Reviewer      string         `json:"reviewer"`
	Appeal        *Appeal        `json:"appeal"`
	AppealUntil   *time.Time     `json:"appeal_until"`
}

const workColumns = `s.id,s.lab_id,l.title,COALESCE(ch.title,''),COALESCE(s.class_id,''),COALESCE(c.name,''),a.display_name,s.submitted_at,
	s.ai_total::float8,s.teacher_total::float8,s.status,s.flags,s.published_at,
	EXISTS (SELECT 1 FROM appeals ap WHERE ap.submission_id=s.id AND ap.status='open')`

const workFrom = `FROM submissions s JOIN labs l ON l.id=s.lab_id LEFT JOIN chapters ch ON ch.id=l.chapter_id
	LEFT JOIN teaching_spaces c ON c.id=s.class_id JOIN accounts a ON a.id=s.account_id`

// rowScanner reads a WorkRow; students only see a grade once published, teachers also their drafts.
func rowScanner(teacher bool) pgx.RowToFunc[WorkRow] {
	return func(row pgx.CollectableRow) (WorkRow, error) {
		var w WorkRow
		var raw string
		var open bool
		err := row.Scan(&w.ID, &w.LabID, &w.LabTitle, &w.Chapter, &w.ClassID, &w.ClassName, &w.Student, &w.SubmittedAt,
			&w.AITotal, &w.TeacherTotal, &raw, &w.Flags, &w.PublishedAt, &open)
		w.Status = status(raw, open)
		if !teacher && w.Status != "published" && w.Status != "appealing" {
			w.TeacherTotal, w.PublishedAt = nil, nil
		}
		return w, err
	}
}

// Works lists the caller's own submissions, newest first.
func (s *Service) Works(ctx context.Context, r account.Roles) ([]WorkRow, error) {
	rows, err := s.db.Query(ctx, "SELECT "+workColumns+" "+workFrom+" WHERE s.account_id=$1 ORDER BY s.submitted_at DESC", r.AccountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, rowScanner(false))
}

// access: the student who submitted, or a teacher of the class.
func (s *Service) access(ctx context.Context, r account.Roles, id string) (student bool, teacher bool, err error) {
	var owner, classID string
	err = s.db.QueryRow(ctx, "SELECT account_id,COALESCE(class_id,'') FROM submissions WHERE id=$1", id).Scan(&owner, &classID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, account.ErrNotFound
	}
	if err != nil {
		return false, false, err
	}
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM teaching_space_members WHERE space_id=$1 AND account_id=$2 AND role='teacher')", classID, r.AccountID).Scan(&teacher); err != nil {
		return false, false, err
	}
	if owner != r.AccountID && !teacher {
		return false, false, account.ErrNotFound
	}
	return owner == r.AccountID, teacher, nil
}

// Work is one submission in full. Students see the teacher's scores only once published; teachers always.
func (s *Service) Work(ctx context.Context, r account.Roles, id string) (Work, error) {
	_, teacher, err := s.access(ctx, r, id)
	if err != nil {
		return Work{}, err
	}
	var w Work
	var raw string
	var open bool
	var scores []byte
	err = s.db.QueryRow(ctx, "SELECT "+workColumns+`,s.files,s.steps,s.one_shot,s.ai_items,s.teacher_scores,s.comment,COALESCE(rv.display_name,'')
		`+workFrom+` LEFT JOIN accounts rv ON rv.id=s.reviewer_id WHERE s.id=$1`, id).Scan(
		&w.ID, &w.LabID, &w.LabTitle, &w.Chapter, &w.ClassID, &w.ClassName, &w.Student, &w.SubmittedAt, &w.AITotal, &w.TeacherTotal, &raw, &w.Flags, &w.PublishedAt, &open,
		&w.Files, &w.Steps, &w.OneShot, &w.Items, &scores, &w.Comment, &w.Reviewer)
	if err != nil {
		return w, err
	}
	w.Status = status(raw, open)
	published := raw == "published"
	if published || teacher {
		_ = json.Unmarshal(scores, &w.TeacherScores)
	}
	if !published && !teacher {
		w.TeacherTotal, w.PublishedAt, w.Comment, w.Reviewer = nil, nil, "", ""
	}
	if published && w.PublishedAt != nil {
		until := w.PublishedAt.Add(AppealWindow)
		w.AppealUntil = &until
	}
	var a Appeal
	err = s.db.QueryRow(ctx, "SELECT id,item,reason,status,reply,created_at,answered_at FROM appeals WHERE submission_id=$1", id).
		Scan(&a.ID, &a.Item, &a.Reason, &a.Status, &a.Reply, &a.CreatedAt, &a.AnsweredAt)
	if err == nil {
		w.Appeal = &a
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return w, err
	}
	return w, nil
}

// Appeal: once per work, against one rubric item, within the appeal window after publishing.
func (s *Service) Appeal(ctx context.Context, r account.Roles, id, item, reason string, now time.Time) (Work, error) {
	student, _, err := s.access(ctx, r, id)
	if err != nil {
		return Work{}, err
	}
	if !student {
		return Work{}, account.ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 500 {
		return Work{}, fmt.Errorf("%w: 请写明理由（500 字以内）", account.ErrRejected)
	}
	w, err := s.Work(ctx, r, id)
	if err != nil {
		return w, err
	}
	if w.Status != "published" || w.AppealUntil == nil || now.After(*w.AppealUntil) {
		return w, fmt.Errorf("%w: 只有教师已发布、且在发布后 7 天内的成绩可以申诉", account.ErrRejected)
	}
	if !slices.ContainsFunc(w.Items, func(it account.Item) bool { return it.Title == item }) {
		return w, fmt.Errorf("%w: 申诉项不在评分项里", account.ErrRejected)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO appeals(id,submission_id,item,reason,demo_account_id) VALUES($1,$2,$3,$4,NULLIF($5,''))`,
		platform.ID("apl"), id, item, reason, r.DemoOwner); err != nil {
		if strings.Contains(err.Error(), "23505") {
			return w, fmt.Errorf("%w: 每份作品只能申诉一次", account.ErrConflict)
		}
		return w, err
	}
	return s.Work(ctx, r, id)
}

type Option struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Submitted int64  `json:"submitted"`
	Pending   int64  `json:"pending"`
}

type ReviewList struct {
	Classes     []Option  `json:"classes"`
	Labs        []Option  `json:"labs"`
	ClassID     string    `json:"class_id"`
	LabID       string    `json:"lab_id"`
	Students    int64     `json:"students"`
	Submissions []WorkRow `json:"submissions"`
}

// taught lists the caller's classes as a teacher with their submission counts.
func (s *Service) taught(ctx context.Context, r account.Roles) ([]Option, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id,c.name,
		(SELECT COUNT(*) FROM submissions s WHERE s.class_id=c.id),(SELECT COUNT(*) FROM submissions s WHERE s.class_id=c.id AND s.status<>'published')
		FROM teaching_spaces c JOIN teaching_space_members m ON m.space_id=c.id AND m.account_id=$1 AND m.role='teacher'
		WHERE c.status='active' ORDER BY c.school_id NULLS LAST,c.name`, r.AccountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Option])
}

func (s *Service) pickClass(ctx context.Context, r account.Roles, classID string) ([]Option, string, error) {
	classes, err := s.taught(ctx, r)
	if err != nil {
		return nil, "", err
	}
	if len(classes) == 0 {
		return classes, "", nil
	}
	if classID == "" {
		return classes, classes[0].ID, nil
	}
	for _, c := range classes {
		if c.ID == classID {
			return classes, classID, nil
		}
	}
	return nil, "", account.ErrNotFound
}

// Review lists one class's submissions for one lab; by default the class and lab with the most to review.
func (s *Service) Review(ctx context.Context, r account.Roles, classID, labID string) (ReviewList, error) {
	var out ReviewList
	var err error
	if out.Classes, out.ClassID, err = s.pickClass(ctx, r, classID); err != nil || out.ClassID == "" {
		return out, err
	}
	rows, err := s.db.Query(ctx, `SELECT l.id,l.title,COUNT(*),COUNT(*) FILTER (WHERE s.status<>'published')
		FROM submissions s JOIN labs l ON l.id=s.lab_id WHERE s.class_id=$1
		GROUP BY l.id ORDER BY 4 DESC, MAX(s.submitted_at) DESC`, out.ClassID)
	if err != nil {
		return out, err
	}
	if out.Labs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Option]); err != nil {
		return out, err
	}
	out.LabID = labID
	if labID == "" && len(out.Labs) > 0 {
		out.LabID = out.Labs[0].ID
	}
	if err := s.db.QueryRow(ctx, "SELECT COUNT(*) FROM teaching_space_members WHERE space_id=$1 AND role='student'", out.ClassID).Scan(&out.Students); err != nil {
		return out, err
	}
	rows, err = s.db.Query(ctx, "SELECT "+workColumns+" "+workFrom+` WHERE s.class_id=$1 AND s.lab_id=$2
		ORDER BY CASE s.status WHEN 'pending' THEN 0 WHEN 'draft' THEN 1 ELSE 2 END, s.submitted_at DESC`, out.ClassID, out.LabID)
	if err != nil {
		return out, err
	}
	out.Submissions, err = pgx.CollectRows(rows, rowScanner(true))
	return out, err
}

// Grade saves a teacher's per-item scores and comment, as a draft or published.
func (s *Service) Grade(ctx context.Context, r account.Roles, id string, scores []int, comment string, publish bool, now time.Time) (Work, error) {
	if _, teacher, err := s.access(ctx, r, id); err != nil || !teacher {
		if err == nil {
			err = account.ErrForbidden
		}
		return Work{}, err
	}
	w, err := s.Work(ctx, r, id)
	if err != nil {
		return w, err
	}
	if len(scores) != len(w.Items) {
		return w, fmt.Errorf("%w: 每个评分项都要给分", account.ErrRejected)
	}
	total := 0
	for i, score := range scores {
		if score < 0 || score > w.Items[i].Max {
			return w, fmt.Errorf("%w: 「%s」的分数要在 0 到 %d 之间", account.ErrRejected, w.Items[i].Title, w.Items[i].Max)
		}
		total += score
	}
	if len([]rune(comment)) > 2000 {
		return w, fmt.Errorf("%w: 评语太长", account.ErrRejected)
	}
	state := "draft"
	var published any
	if publish {
		state, published = "published", now
	} else if w.Status == "published" || w.Status == "appealing" {
		state, published = "published", w.PublishedAt // saving an already published grade keeps it published
	}
	raw, _ := json.Marshal(scores)
	if _, err := s.db.Exec(ctx, `UPDATE submissions SET teacher_scores=$2,teacher_total=$3,comment=$4,status=$5,reviewer_id=$6,
		published_at=$7 WHERE id=$1`, id, raw, total, strings.TrimSpace(comment), state, r.AccountID, published); err != nil {
		return w, err
	}
	return s.Work(ctx, r, id)
}

// Publish releases several grades at once; works the teacher has not scored keep the AI scores.
func (s *Service) Publish(ctx context.Context, r account.Roles, ids []string, now time.Time) (int64, error) {
	if len(ids) == 0 || len(ids) > 500 {
		return 0, fmt.Errorf("%w: 一次发布 1 到 500 份", account.ErrRejected)
	}
	tag, err := s.db.Exec(ctx, `UPDATE submissions s SET status='published',published_at=$3,reviewer_id=$2,
		teacher_scores=COALESCE(s.teacher_scores,(SELECT jsonb_agg((it->>'score')::int ORDER BY o) FROM jsonb_array_elements(s.ai_items) WITH ORDINALITY AS t(it,o))),
		teacher_total=COALESCE(s.teacher_total,s.ai_total)
		WHERE s.id=ANY($1) AND s.status<>'published'
		AND s.class_id IN (SELECT space_id FROM teaching_space_members WHERE account_id=$2 AND role='teacher')`, ids, r.AccountID, now)
	return tag.RowsAffected(), err
}

type Gradebook struct {
	Classes []Option       `json:"classes"`
	ClassID string         `json:"class_id"`
	Labs    []Option       `json:"labs"`
	Rows    []GradebookRow `json:"rows"`
}

type GradebookRow struct {
	StudentID string     `json:"student_id"`
	Name      string     `json:"name"`
	Scores    []*float64 `json:"scores"`   // per lab: the published grade, or null
	Statuses  []string   `json:"statuses"` // per lab: missing | pending | draft | published | appealing
	Average   *float64   `json:"average"`
}

func (s *Service) Gradebook(ctx context.Context, r account.Roles, classID string) (Gradebook, error) {
	var g Gradebook
	var err error
	if g.Classes, g.ClassID, err = s.pickClass(ctx, r, classID); err != nil || g.ClassID == "" {
		return g, err
	}
	rows, err := s.db.Query(ctx, `SELECT l.id,l.title,COUNT(*),COUNT(*) FILTER (WHERE s.status<>'published')
		FROM submissions s JOIN labs l ON l.id=s.lab_id WHERE s.class_id=$1 GROUP BY l.id ORDER BY MIN(l.position)`, g.ClassID)
	if err != nil {
		return g, err
	}
	if g.Labs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Option]); err != nil {
		return g, err
	}
	index := map[string]int{}
	for i, l := range g.Labs {
		index[l.ID] = i
	}
	rows, err = s.db.Query(ctx, `SELECT a.id,a.display_name,s.lab_id,s.status,s.teacher_total::float8,
		EXISTS (SELECT 1 FROM appeals ap WHERE ap.submission_id=s.id AND ap.status='open')
		FROM teaching_space_members m JOIN accounts a ON a.id=m.account_id
		LEFT JOIN submissions s ON s.account_id=a.id AND s.class_id=m.space_id
		WHERE m.space_id=$1 AND m.role='student' ORDER BY a.display_name,a.id`, g.ClassID)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		var lab, raw *string
		var total *float64
		var open bool
		if err := rows.Scan(&id, &name, &lab, &raw, &total, &open); err != nil {
			return g, err
		}
		if len(g.Rows) == 0 || g.Rows[len(g.Rows)-1].StudentID != id {
			row := GradebookRow{StudentID: id, Name: name, Scores: make([]*float64, len(g.Labs)), Statuses: slices.Repeat([]string{"missing"}, len(g.Labs))}
			g.Rows = append(g.Rows, row)
		}
		if lab == nil {
			continue
		}
		row := &g.Rows[len(g.Rows)-1]
		i := index[*lab]
		row.Statuses[i] = status(*raw, open)
		if *raw == "published" {
			row.Scores[i] = total
		}
	}
	for i := range g.Rows {
		sum, n := 0.0, 0
		for _, v := range g.Rows[i].Scores {
			if v != nil {
				sum, n = sum+*v, n+1
			}
		}
		if n > 0 {
			avg := math.Round(sum/float64(n)*10) / 10
			g.Rows[i].Average = &avg
		}
	}
	return g, rows.Err()
}

type AppealRow struct {
	Appeal
	SubmissionID string   `json:"submission_id"`
	Student      string   `json:"student"`
	LabTitle     string   `json:"lab_title"`
	ClassName    string   `json:"class_name"`
	Total        *float64 `json:"teacher_total"`
}

// Appeals lists appeals on the caller's classes, open ones first.
func (s *Service) Appeals(ctx context.Context, r account.Roles) ([]AppealRow, error) {
	rows, err := s.db.Query(ctx, `SELECT ap.id,ap.item,ap.reason,ap.status,ap.reply,ap.created_at,ap.answered_at,
		s.id,a.display_name,l.title,COALESCE(c.name,''),s.teacher_total::float8
		FROM appeals ap JOIN submissions s ON s.id=ap.submission_id JOIN accounts a ON a.id=s.account_id
		JOIN labs l ON l.id=s.lab_id LEFT JOIN teaching_spaces c ON c.id=s.class_id
		WHERE s.class_id IN (SELECT space_id FROM teaching_space_members WHERE account_id=$1 AND role='teacher')
		ORDER BY ap.status='answered', ap.created_at DESC`, r.AccountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AppealRow, error) {
		var a AppealRow
		err := row.Scan(&a.ID, &a.Item, &a.Reason, &a.Status, &a.Reply, &a.CreatedAt, &a.AnsweredAt, &a.SubmissionID, &a.Student, &a.LabTitle, &a.ClassName, &a.Total)
		return a, err
	})
}

// Answer replies to an appeal; the teacher changes scores through Grade first when the appeal is upheld.
func (s *Service) Answer(ctx context.Context, r account.Roles, id, reply string, now time.Time) error {
	reply = strings.TrimSpace(reply)
	if reply == "" || len([]rune(reply)) > 1000 {
		return fmt.Errorf("%w: 请写回复（1000 字以内）", account.ErrRejected)
	}
	tag, err := s.db.Exec(ctx, `UPDATE appeals ap SET reply=$2,status='answered',answered_at=$4
		FROM submissions s WHERE ap.id=$1 AND s.id=ap.submission_id
		AND s.class_id IN (SELECT space_id FROM teaching_space_members WHERE account_id=$3 AND role='teacher')`, id, reply, r.AccountID, now)
	if err == nil && tag.RowsAffected() == 0 {
		return account.ErrNotFound
	}
	return err
}
