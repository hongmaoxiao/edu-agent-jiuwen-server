package account

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"edu-agent-jiuwen-server/internal/platform"
)

// demodata.json: the original 智教云 demo sample (schools, models and prices, class structure) and the grading
// examples from the design, so a demo shows a working platform rather than empty pages. See its _source.
//
//go:embed demodata.json
var demoDataRaw []byte

type demoData struct {
	Schools []struct {
		Name, City, Kind, Contact string
		Quota                     int64
		Size                      float64
	}
	Majors      []string
	Term        string
	SocialClass struct {
		Name     string
		Students int
		Quota    int64
	} `json:"social_class"`
	Models []struct {
		Name, Provider, Description string
		InputPrice                  float64 `json:"input_price"`
		OutputPrice                 float64 `json:"output_price"`
		Share                       float64
	}
	Surnames, Given, Teachers []string
	ToolLab                   struct {
		SourceID string `json:"source_id"`
		Files    [][2]string
		Good     demoTemplate
		OneShot  demoTemplate `json:"one_shot"`
	} `json:"tool_lab"`
	GenericItems  [][]any  `json:"generic_items"`
	CommentsGood  []string `json:"comments_good"`
	CommentsOK    []string `json:"comments_ok"`
	AppealReasons []string `json:"appeal_reasons"`
	AppealReply   string   `json:"appeal_reply"`
}

// demoTemplate rows are JSON arrays as written in the design: items [title,max,score,reason,refs,tip,capped?],
// steps [title, minutes before submitting or null, instruction, question?, answer?].
type demoTemplate struct {
	Items [][]any
	Steps [][]any
}

// Item is one rubric line of an AI evaluation, as the client shows it.
type Item struct {
	Title  string `json:"title"`
	Max    int    `json:"max"`
	Score  int    `json:"score"`
	Reason string `json:"reason"`
	Refs   []Ref  `json:"refs"`
	Tip    string `json:"tip"`
	Capped bool   `json:"capped,omitempty"`
}

type Ref struct {
	Text     string `json:"text"`
	Verified bool   `json:"verified"`
}

type Step struct {
	Title       string `json:"title"`
	Time        string `json:"time"`
	Instruction string `json:"instruction"`
	Question    string `json:"question,omitempty"`
	Answer      string `json:"answer,omitempty"`
}

type File struct {
	Name string `json:"name"`
	Size string `json:"size"`
}

type demoLab struct {
	id, sourceID, title string
	deliverables        []string
	steps               []struct{ Title, Question string }
}

// demoSeed builds one demo's teaching data. Everything is owned by the demo account and dated relative to now,
// so "today" and "this month" always have numbers; a reset deletes it and builds it again.
type demoSeed struct {
	tx         pgx.Tx
	ctx        context.Context
	demo       string
	login      string
	now        time.Time
	rng        *rand.Rand
	data       demoData
	course     *string
	labs       []demoLab
	tool       *demoLab
	models     []string
	used       map[string]bool
	accounts   [][]any
	spaces     [][]any
	members    [][]any
	classSeats [][]any
	usage      [][]any
	subs       [][]any
	serial     int
}

type demoStudent struct {
	id, name string
	activity float64 // how much this student uses the agent, around 1
	ability  float64 // how well they do, 0–1
}

type seedClass struct {
	id, school string
	students   []demoStudent
}

func seedDemoData(ctx context.Context, tx pgx.Tx, demo, login string, now time.Time) error {
	s := &demoSeed{tx: tx, ctx: ctx, demo: demo, login: limit(login, 44), now: now, used: map[string]bool{},
		rng: rand.New(rand.NewPCG(20261115, 1))}
	if err := json.Unmarshal(demoDataRaw, &s.data); err != nil {
		return err
	}
	var school, classID, teacher, student string
	if err := tx.QueryRow(ctx, `SELECT d.school_id,d.class_id,
		(SELECT persona_account_id FROM demo_personas WHERE demo_account_id=$1 AND perspective='teacher'),
		(SELECT persona_account_id FROM demo_personas WHERE demo_account_id=$1 AND perspective='student')
		FROM demo_setups d WHERE d.account_id=$1`, demo).Scan(&school, &classID, &teacher, &student); err != nil {
		return err
	}
	if err := s.loadCourse(); err != nil {
		return err
	}
	if err := s.insertModels(); err != nil {
		return err
	}

	var taught []seedClass
	for i, sc := range s.data.Schools {
		id := school
		if i == 0 {
			// The demo school the personas belong to becomes the first sample school.
			if _, err := tx.Exec(ctx, "UPDATE schools SET name=$2,city=$3,kind=$4,contact=$5 WHERE id=$1", id, sc.Name, sc.City, sc.Kind, sc.Contact); err != nil {
				return err
			}
		} else {
			id = platform.ID("sch")
			if _, err := tx.Exec(ctx, "INSERT INTO schools(id,name,city,kind,contact,is_demo,demo_account_id) VALUES($1,$2,$3,$4,$5,true,$6)",
				id, sc.Name, sc.City, sc.Kind, sc.Contact, demo); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "INSERT INTO budgets(id,school_id,monthly_tokens,demo_account_id) VALUES($1,$2,$3,$4)", platform.ID("bud"), id, sc.Quota, demo); err != nil {
			return err
		}
		admin := s.person("t", s.data.Teachers[i%len(s.data.Teachers)])
		s.members = append(s.members, []any{id, admin, "school_admin"}, []any{id, admin, "teacher"})
		for j, major := range s.data.Majors {
			classTeacher := admin
			if i == 0 {
				classTeacher = teacher // the demo teacher teaches both classes of the first school
			} else if j == 1 {
				classTeacher = s.person("t", s.data.Teachers[(i+6)%len(s.data.Teachers)])
				s.members = append(s.members, []any{id, classTeacher, "teacher"})
			}
			class := seedClass{id: platform.ID("cls"), school: id}
			s.spaces = append(s.spaces, []any{class.id, "智能体部署 · " + major + " 2401 班", id, classTeacher, s.course, s.data.Term, demo})
			s.classSeats = append(s.classSeats, []any{class.id, classTeacher, "teacher"})
			n := int(math.Round(36*sc.Size)) + s.rng.IntN(7) + 8
			if i == 0 && j == 0 {
				class.students = append(class.students, demoStudent{id: student, name: "演示学生", activity: 1.1, ability: 0.86})
				s.classSeats = append(s.classSeats, []any{class.id, student, "student"})
			}
			for range n {
				st := s.student(class.id)
				s.members = append(s.members, []any{id, st.id, "student"})
				class.students = append(class.students, st)
			}
			s.usageFor(class)
			if i == 0 {
				taught = append(taught, class)
			}
		}
		s.history(id, sc.Size)
	}

	// The demo social class: no school, quota granted by the operator; the demo teacher teaches it.
	social := seedClass{id: classID}
	if _, err := tx.Exec(ctx, "UPDATE teaching_spaces SET name=$2,course_id=$3,term=$4 WHERE id=$1", classID, s.data.SocialClass.Name, s.course, s.data.Term); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.display_name FROM teaching_space_members m JOIN accounts a ON a.id=m.account_id
		WHERE m.space_id=$1 AND m.role='student' ORDER BY a.login`, classID)
	if err != nil {
		return err
	}
	existing, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (demoStudent, error) {
		var st demoStudent
		err := row.Scan(&st.id, &st.name)
		st.activity, st.ability = 0.7+s.rng.Float64()*0.6, 0.6+s.rng.Float64()*0.35
		return st, err
	})
	if err != nil {
		return err
	}
	social.students = existing
	for range s.data.SocialClass.Students - len(existing) {
		social.students = append(social.students, s.student(classID))
	}
	if _, err := tx.Exec(ctx, "INSERT INTO budgets(id,class_id,monthly_tokens,demo_account_id) VALUES($1,$2,$3,$4)", platform.ID("bud"), classID, s.data.SocialClass.Quota, demo); err != nil {
		return err
	}
	s.usageFor(social)
	taught = append(taught, social)

	if err := s.copy("accounts", []string{"id", "login", "display_name", "password_hash", "must_change_password", "is_demo", "demo_account_id"}, s.accounts); err != nil {
		return err
	}
	if err := s.copy("teaching_spaces", []string{"id", "name", "school_id", "opened_by", "course_id", "term", "demo_account_id"}, s.spaces); err != nil {
		return err
	}
	if err := s.copy("school_members", []string{"school_id", "account_id", "role"}, s.members); err != nil {
		return err
	}
	if err := s.copy("teaching_space_members", []string{"space_id", "account_id", "role"}, s.classSeats); err != nil {
		return err
	}
	if err := s.copy("usage_records", []string{"day", "account_id", "school_id", "class_id", "lab_id", "model_id", "calls", "input_tokens", "output_tokens", "cost", "demo_account_id"}, s.usage); err != nil {
		return err
	}
	if s.tool == nil {
		return nil // no FDE content imported (tests without content): no works to show
	}
	for i, class := range taught {
		s.works(class, teacher, i == 0)
	}
	if err := s.copy("submissions", []string{"id", "account_id", "class_id", "lab_id", "submitted_at", "files", "steps", "one_shot", "flags", "ai_items", "ai_total",
		"teacher_scores", "teacher_total", "comment", "status", "reviewer_id", "published_at", "demo_account_id"}, s.subs); err != nil {
		return err
	}
	return s.appeals(student)
}

func (s *demoSeed) copy(table string, columns []string, rows [][]any) error {
	_, err := s.tx.CopyFrom(s.ctx, pgx.Identifier{table}, columns, pgx.CopyFromRows(rows))
	return err
}

func (s *demoSeed) loadCourse() error {
	err := s.tx.QueryRow(s.ctx, "SELECT id FROM courses WHERE source_id='course-fde'").Scan(&s.course)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := s.tx.Query(s.ctx, "SELECT id,source_id,title,deliverables,guide_steps FROM labs WHERE course_id=$1 ORDER BY position", *s.course)
	if err != nil {
		return err
	}
	s.labs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (demoLab, error) {
		var l demoLab
		err := row.Scan(&l.id, &l.sourceID, &l.title, &l.deliverables, &l.steps)
		return l, err
	})
	for i := range s.labs {
		if s.labs[i].sourceID == s.data.ToolLab.SourceID {
			s.tool = &s.labs[i]
		}
	}
	return err
}

func (s *demoSeed) insertModels() error {
	for i, m := range s.data.Models {
		id := platform.ID("mdl")
		if _, err := s.tx.Exec(s.ctx, "INSERT INTO models(id,name,provider,description,input_price,output_price,position,demo_account_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)",
			id, m.Name, m.Provider, m.Description, m.InputPrice, m.OutputPrice, i, s.demo); err != nil {
			return err
		}
		s.models = append(s.models, id)
	}
	return nil
}

// person opens a demo account that cannot sign in (like the personas); kind is t for teachers, s for students.
func (s *demoSeed) person(kind, name string) string {
	s.serial++
	id := platform.ID("acc")
	s.accounts = append(s.accounts, []any{id, fmt.Sprintf("%s.%s%04d", s.login, kind, s.serial), name, "!", false, true, s.demo})
	return id
}

func (s *demoSeed) student(class string) demoStudent {
	var name string
	for {
		name = s.data.Surnames[s.rng.IntN(len(s.data.Surnames))] + s.data.Given[s.rng.IntN(len(s.data.Given))]
		if !s.used[name] || len(s.used) > 2000 {
			break
		}
	}
	s.used[name] = true
	st := demoStudent{id: s.person("s", name), name: name, activity: 0.45 + s.rng.Float64()*0.9, ability: 0.55 + s.rng.Float64()*0.4}
	s.classSeats = append(s.classSeats, []any{class, st.id, "student"})
	return st
}

func (s *demoSeed) day(back int) time.Time {
	y, m, d := s.now.In(beijing).Date()
	return time.Date(y, m, d-back, 0, 0, 0, 0, time.UTC)
}

var beijing = time.FixedZone("CST", 8*3600)

func (s *demoSeed) model() (int, string) {
	x := s.rng.Float64()
	for i, m := range s.data.Models {
		if x -= m.Share; x < 0 {
			return i, s.models[i]
		}
	}
	return len(s.models) - 1, s.models[len(s.models)-1]
}

func (s *demoSeed) cost(model int, in, out int64) float64 {
	m := s.data.Models[model]
	return math.Round((float64(in)*m.InputPrice+float64(out)*m.OutputPrice)/1e6*10000) / 10000
}

// usageFor: the last 30 days, one row per student per active day. Weekends are quieter and the term ramps up.
func (s *demoSeed) usageFor(class seedClass) {
	for back := 30; back >= 0; back-- {
		day := s.day(back)
		weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
		for _, st := range class.students {
			chance := 0.62 * st.activity
			if weekend {
				chance = 0.18 * st.activity
			}
			if s.rng.Float64() > chance {
				continue
			}
			tokens := int64((180000 + s.rng.Float64()*420000) * st.activity * (0.85 + float64(30-back)*0.008))
			if back == 0 {
				tokens = tokens * int64(8+s.now.In(beijing).Hour()) / 24 // today is still under way
			}
			in := tokens * 78 / 100
			mi, model := s.model()
			var lab any
			if n := len(s.labs); n > 0 {
				lab = s.labs[min(n-1, (60-back)/7)].id
			}
			var school any
			if class.school != "" {
				school = class.school
			}
			s.usage = append(s.usage, []any{day, st.id, school, class.id, lab, model, int32(tokens/9000 + 1), in, tokens - in, s.cost(mi, in, tokens-in), s.demo})
		}
	}
}

// history: before the last 30 days only school totals were migrated, one row per school, day and model.
func (s *demoSeed) history(school string, size float64) {
	for back := 180; back > 30; back-- {
		day := s.day(back)
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday || (back > 75 && back < 105) { // summer break
			continue
		}
		total := 9e6 * size * (0.6 + 0.4*float64(180-back)/150) * (0.8 + s.rng.Float64()*0.4)
		for mi, m := range s.data.Models {
			tokens := int64(total * m.Share)
			in := tokens * 78 / 100
			s.usage = append(s.usage, []any{day, nil, school, nil, nil, s.models[mi], int32(tokens/9000 + 1), in, tokens - in, s.cost(mi, in, tokens-in), s.demo})
		}
	}
}

// works: earlier labs are graded and published; the tool-call lab is the current assignment, mostly awaiting review.
func (s *demoSeed) works(class seedClass, teacher string, first bool) {
	earlier := s.labs[:min(6, len(s.labs))]
	for _, st := range class.students {
		for k, lab := range earlier {
			if s.rng.Float64() > 0.93 && st.name != "演示学生" {
				continue
			}
			// weekly labs; the latest one was graded two days ago, so it can still be appealed
			at := s.now.Add(-time.Duration(7*(len(earlier)-k)-4)*24*time.Hour - time.Duration(s.rng.IntN(600))*time.Minute)
			items := s.genericItems(lab, st.ability)
			s.add(st, class, lab, at, items, false, true, teacher)
		}
		if s.rng.Float64() < 0.22 && st.name != "演示学生" {
			continue // not submitted yet
		}
		at := s.now.Add(-time.Duration(s.rng.IntN(3*24*60)) * time.Minute)
		oneShot := s.rng.Float64() < 0.1 && st.name != "演示学生"
		items := s.toolItems(st.ability, oneShot)
		published := !first || s.rng.Float64() < 0.25
		if st.name == "演示学生" {
			published = false
		}
		s.add(st, class, *s.tool, at, items, oneShot, published, teacher)
	}
	// One work the teacher started reviewing but has not published.
	for i := len(s.subs) - 1; i >= 0; i-- {
		if row := s.subs[i]; row[3] == s.tool.id && row[14] == "pending" && row[2] == class.id {
			row[14], row[11], row[12], row[13] = "draft", scoresJSON(row[9]), row[10], s.data.CommentsGood[0]
			break
		}
	}
}

func (s *demoSeed) add(st demoStudent, class seedClass, lab demoLab, at time.Time, items []Item, oneShot, published bool, teacher string) {
	total := 0
	for _, it := range items {
		total += it.Score
	}
	var flags []string
	if oneShot {
		flags = append(flags, "一次性完成")
	}
	if slices.ContainsFunc(items, func(it Item) bool { return slices.ContainsFunc(it.Refs, func(r Ref) bool { return !r.Verified }) }) {
		flags = append(flags, "引用未核实")
	}
	if total < 50 && !oneShot {
		flags = append(flags, "分数偏离")
	}
	files, steps := s.material(lab, at, oneShot)
	row := []any{platform.ID("sub"), st.id, class.id, lab.id, at, mustJSON(files), mustJSON(steps), oneShot, mustJSON(flags), mustJSON(items), total,
		nil, nil, "", "pending", nil, nil, s.demo}
	if published {
		scores := make([]int, len(items))
		teacherTotal := 0
		for i, it := range items {
			scores[i] = max(0, min(it.Max, it.Score+[]int{0, 0, 0, 1, -1, 2}[s.rng.IntN(6)]))
			teacherTotal += scores[i]
		}
		comments := s.data.CommentsOK
		if teacherTotal >= 82 {
			comments = s.data.CommentsGood
		}
		row[11], row[12], row[13], row[14], row[15], row[16] = mustJSON(scores), teacherTotal, comments[s.rng.IntN(len(comments))], "published", teacher, at.Add(26*time.Hour)
	}
	s.subs = append(s.subs, row)
}

func (s *demoSeed) material(lab demoLab, at time.Time, oneShot bool) ([]File, []Step) {
	clock := func(minutes any) string {
		if minutes == nil {
			return "—"
		}
		return at.Add(time.Duration(minutes.(float64)) * time.Minute).In(beijing).Format("15:04")
	}
	var files []File
	var steps []Step
	if lab.sourceID == s.data.ToolLab.SourceID {
		for _, f := range s.data.ToolLab.Files {
			files = append(files, File{Name: f[0], Size: f[1]})
		}
		template := s.data.ToolLab.Good
		if oneShot {
			template = s.data.ToolLab.OneShot
		}
		for _, row := range template.Steps {
			st := Step{Title: row[0].(string), Time: clock(row[1]), Instruction: row[2].(string)}
			if len(row) > 4 {
				st.Question, st.Answer = row[3].(string), row[4].(string)
			}
			steps = append(steps, st)
		}
		return files, steps
	}
	for _, d := range lab.deliverables {
		files = append(files, File{Name: d, Size: fmt.Sprintf("%.1f KB", 0.8+s.rng.Float64()*5)})
	}
	for i, g := range lab.steps {
		st := Step{Title: g.Title, Time: clock(float64(-40 + i*9)), Instruction: "这一步只做「" + g.Title + "」：先列出要用到的材料，结果写进对应的交付物，不要改动其他文件。"}
		if g.Question != "" {
			st.Question, st.Answer = g.Question, "逐条对照 LAB.md 核对了一遍，发现有一处结论没有依据，让智能体补上了来源。"
		}
		steps = append(steps, st)
	}
	return files, steps
}

func (s *demoSeed) toolItems(ability float64, oneShot bool) []Item {
	template := s.data.ToolLab.Good
	if oneShot {
		template = s.data.ToolLab.OneShot
	}
	f := ability/0.8 + (s.rng.Float64()-0.5)*0.12
	var items []Item
	for _, row := range template.Items {
		it := Item{Title: row[0].(string), Max: int(row[1].(float64)), Reason: row[3].(string), Tip: row[5].(string), Capped: len(row) > 6}
		it.Score = int(row[2].(float64))
		if !oneShot {
			it.Score = max(0, min(it.Max, int(math.Round(row[2].(float64)*f))))
		}
		for _, r := range row[4].([]any) {
			ref := r.([]any)
			it.Refs = append(it.Refs, Ref{Text: ref[0].(string), Verified: ref[1].(bool) || s.rng.Float64() < 0.5})
		}
		items = append(items, it)
	}
	return items
}

func (s *demoSeed) genericItems(lab demoLab, ability float64) []Item {
	deliverables := strings.Join(lab.deliverables, "、")
	weak := ""
	if len(lab.deliverables) > 0 {
		weak = lab.deliverables[len(lab.deliverables)-1]
	}
	stepTitle := func(i int) string {
		if i < len(lab.steps) {
			return lab.steps[i].Title
		}
		return "方案比较"
	}
	turns := 3 + s.rng.IntN(3)
	fill := strings.NewReplacer("{deliverables}", deliverables, "{weak}", weak, "{step2}", stepTitle(1), "{step3}", stepTitle(2),
		"{turns}", fmt.Sprint(turns))
	var items []Item
	for _, row := range s.data.GenericItems {
		q := min(1, max(0.3, ability+(s.rng.Float64()-0.5)*0.2))
		it := Item{Title: row[0].(string), Max: int(row[1].(float64)), Tip: fill.Replace(row[4].(string))}
		it.Score = int(math.Round(float64(it.Max) * q))
		it.Reason = fill.Replace(row[2].(string))
		if q < 0.8 {
			it.Reason = fill.Replace(row[3].(string))
		}
		ref := weak
		if ref == "" {
			ref = "LAB.md"
		}
		it.Refs = []Ref{{Text: ref, Verified: true}}
		items = append(items, it)
	}
	return items
}

// appeals: the demo student appealed one published work; one more student is waiting for an answer and an
// earlier appeal was answered, so the teacher's appeal list shows both states.
func (s *demoSeed) appeals(student string) error {
	var mine, other, answered string
	for _, row := range s.subs {
		if row[14] != "published" {
			continue
		}
		switch {
		case row[1] == student && mine == "" && row[3] == s.labs[min(2, len(s.labs)-1)].id:
			mine = row[0].(string)
		case row[1] != student && row[3] == s.tool.id && other == "":
			other = row[0].(string)
		case row[1] != student && answered == "" && row[3] == s.labs[0].id:
			answered = row[0].(string)
		}
	}
	for i, id := range []string{mine, other, answered} {
		if id == "" {
			continue
		}
		item := "核查纠偏"
		if i == 0 {
			item = "说明与证据"
		}
		status, reply := "open", ""
		var at, answeredAt any = s.now.Add(-time.Duration(20+i*30) * time.Hour), nil
		if i == 2 {
			status, reply, answeredAt = "answered", s.data.AppealReply, s.now.Add(-48*time.Hour)
			at = s.now.Add(-96 * time.Hour)
		}
		if _, err := s.tx.Exec(s.ctx, `INSERT INTO appeals(id,submission_id,item,reason,status,reply,created_at,answered_at,demo_account_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, platform.ID("apl"), id, item, s.data.AppealReasons[i%len(s.data.AppealReasons)], status, reply, at, answeredAt, s.demo); err != nil {
			return err
		}
	}
	return nil
}

func scoresJSON(items any) []byte {
	var parsed []Item
	_ = json.Unmarshal(items.([]byte), &parsed)
	scores := make([]int, len(parsed))
	for i, it := range parsed {
		scores[i] = it.Score
	}
	return mustJSON(scores)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	if string(b) == "null" {
		return []byte("[]")
	}
	return b
}
