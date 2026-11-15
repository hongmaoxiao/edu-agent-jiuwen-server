package gateway

import (
	"context"
	"os"
	"testing"

	"edu-agent-jiuwen-server/internal/content"
)

// withContent imports the FDE course the demo's works refer to.
func (h *harness) withContent() {
	h.t.Helper()
	raw, err := os.ReadFile("../../seed/fde-content.json")
	if err != nil {
		h.t.Fatal(err)
	}
	export, err := content.Decode(raw)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := content.Import(context.Background(), h.db, export); err != nil {
		h.t.Fatal(err)
	}
}

func (r reply) list(key string) []any {
	v, _ := r.body[key].([]any)
	return v
}

func (r reply) num(key string) float64 {
	v, _ := r.body[key].(float64)
	return v
}

func TestDemoShowsMigratedNumbersThatRealAccountsNeverSee(t *testing.T) {
	h := newHarness(t)
	h.withContent()
	ops := h.mustLogin("ops", "operator-password", "desktop")
	h.school(ops, "真实学校")
	h.demo("demo", "客户演示")
	token := h.mustLogin("demo", "demo-password", "web")

	o := h.do("GET", "/v1/overview?scope=platform", token, nil, nil)
	if o.code != 200 || o.num("today_tokens") <= 0 || o.num("month_tokens") <= 0 || o.num("total_tokens") <= o.num("month_tokens") ||
		o.num("schools") != 8 || o.num("social_classes") != 1 || o.num("students") < 200 || len(o.list("trend")) != 30 || len(o.list("models")) != 4 || len(o.list("rank")) < 9 {
		t.Fatalf("demo overview must be full of migrated sample data: %d %v", o.code, o.body)
	}
	schools := h.do("GET", "/v1/schools/stats", token, nil, nil).list("schools")
	school := schools[0].(map[string]any)
	if len(schools) != 8 || school["students"].(float64) < 20 || school["monthly_quota"] == nil {
		t.Errorf("demo school list: %v", schools)
	}
	if o := h.do("GET", "/v1/overview?scope=school&school_id="+school["id"].(string), token, nil, nil); o.code != 200 || len(o.list("rank")) != 2 {
		t.Errorf("a school ranks its classes: %d %v", o.code, o.body)
	}
	if o := h.do("GET", "/v1/overview?scope=social", token, nil, nil); o.code != 200 || o.num("classes") != 1 || o.num("month_tokens") <= 0 {
		t.Errorf("social scope: %d %v", o.code, o.body)
	}
	for _, group := range []string{"school", "class", "lab", "model", "student", "day"} {
		if u := h.do("GET", "/v1/usage?scope=platform&days=30&group="+group, token, nil, nil); u.code != 200 || len(u.list("rows")) == 0 {
			t.Errorf("usage by %s: %d %v", group, u.code, u.body)
		}
	}
	if m := h.do("GET", "/v1/models", token, nil, nil).list("models"); len(m) != 4 || m[0].(map[string]any)["month_tokens"].(float64) <= 0 {
		t.Errorf("demo models: %v", m)
	}
	if b := h.do("GET", "/v1/budgets", token, nil, nil).list("budgets"); len(b) != 9 {
		t.Errorf("each sample school and the social class has a budget: %v", b)
	}

	// 真实运营看不到任何演示数据。
	real := h.do("GET", "/v1/overview?scope=platform", ops, nil, nil)
	if real.code != 200 || real.num("total_tokens") != 0 || real.num("schools") != 1 || real.num("students") != 0 || len(real.list("rank")) != 0 {
		t.Errorf("the real operator must not see demo numbers: %v", real.body)
	}
	for _, path := range []string{"/v1/models", "/v1/budgets"} {
		for key, v := range h.do("GET", path, ops, nil, nil).body {
			if items, _ := v.([]any); len(items) != 0 {
				t.Errorf("%s %s leaks demo rows to the real operator: %v", path, key, items)
			}
		}
	}
	if s := h.do("GET", "/v1/schools/stats", ops, nil, nil).list("schools"); len(s) != 1 {
		t.Errorf("the real operator sees only real schools: %v", s)
	}
	if u := h.do("GET", "/v1/usage?scope=school&school_id="+school["id"].(string)+"&group=day", ops, nil, nil); u.code != 404 {
		t.Errorf("the real operator reached a demo school's usage: %d", u.code)
	}
}

func TestDemoTeacherReviewsAndStudentAppeals(t *testing.T) {
	h := newHarness(t)
	h.withContent()
	h.demo("demo", "客户演示")
	login := h.mustLogin("demo", "demo-password", "web")
	teacher := h.switchTo(login, "teacher").str("access_token")

	review := h.do("GET", "/v1/review", teacher, nil, nil)
	subs := review.list("submissions")
	if review.code != 200 || len(review.list("classes")) != 3 || len(review.list("labs")) < 2 || len(subs) < 20 {
		t.Fatalf("demo teacher has works to review: %d %v", review.code, review.body)
	}
	first := subs[0].(map[string]any)
	if first["status"] != "pending" {
		t.Errorf("pending works come first: %v", first)
	}
	detail := h.do("GET", "/v1/works/"+first["id"].(string), teacher, nil, nil)
	items := detail.list("items")
	if detail.code != 200 || len(items) != 6 || len(detail.list("files")) != 5 || len(detail.list("steps")) != 5 {
		t.Fatalf("a tool-call work shows files, steps and six rubric items: %v", detail.body)
	}
	scores := make([]int, len(items))
	for i, it := range items {
		scores[i] = int(it.(map[string]any)["score"].(float64))
	}
	scores[0]++
	if items[0].(map[string]any)["max"].(float64) < float64(scores[0]) {
		scores[0]--
	}
	graded := h.do("PUT", "/v1/review/"+first["id"].(string), teacher, map[string]any{"scores": scores, "comment": "很好", "publish": true}, nil)
	if graded.code != 200 || graded.str("status") != "published" || graded.body["teacher_total"] == nil {
		t.Errorf("publishing a grade: %d %v", graded.code, graded.body)
	}
	if r := h.do("PUT", "/v1/review/"+first["id"].(string), teacher, map[string]any{"scores": []int{999, 0, 0, 0, 0, 0}}, nil); r.code != 400 {
		t.Errorf("scores above the item maximum must be rejected: %d", r.code)
	}
	var pending []string
	for _, s := range subs[1:] {
		if s := s.(map[string]any); s["status"] == "pending" && s["student"] != "演示学生" { // leave the student's current work under review
			pending = append(pending, s["id"].(string))
		}
	}
	if r := h.do("POST", "/v1/review/publish", teacher, map[string]any{"ids": pending}, nil); r.code != 200 || r.num("published") != float64(len(pending)) {
		t.Errorf("batch publish: %d %v (want %d)", r.code, r.body, len(pending))
	}
	book := h.do("GET", "/v1/gradebook", teacher, nil, nil)
	if book.code != 200 || len(book.list("labs")) < 2 || len(book.list("rows")) < 20 {
		t.Errorf("gradebook: %d %v", book.code, book.body)
	}
	appeals := h.do("GET", "/v1/appeals", teacher, nil, nil).list("appeals")
	if len(appeals) < 3 || appeals[0].(map[string]any)["status"] != "open" {
		t.Fatalf("open appeals come first: %v", appeals)
	}
	if r := h.do("POST", "/v1/appeals/"+appeals[0].(map[string]any)["id"].(string)+"/answer", teacher, map[string]any{"reply": "已复核，维持原分。"}, nil); r.code != 204 {
		t.Errorf("answering an appeal: %d %v", r.code, r.body)
	}

	student := h.switchTo(teacher, "student").str("access_token") // switching retires the previous token
	works := h.do("GET", "/v1/works", student, nil, nil).list("works")
	if len(works) < 7 {
		t.Fatalf("the demo student has earlier graded works and the current one: %v", works)
	}
	for _, w := range works {
		if w.(map[string]any)["class_name"] == socialClass {
			t.Fatalf("the demo student is only in 人工智能 2401, not the weekend class: %v", w)
		}
	}
	var reviewing, appealable string
	for _, w := range works {
		w := w.(map[string]any)
		switch {
		case w["status"] == "pending" && reviewing == "":
			reviewing = w["id"].(string)
			if w["teacher_total"] != nil {
				t.Errorf("a grade must stay hidden until published: %v", w)
			}
		case w["status"] == "published" && appealable == "":
			appealable = w["id"].(string)
		}
	}
	mine := h.do("GET", "/v1/works/"+appealable, student, nil, nil)
	item := mine.list("items")[0].(map[string]any)["title"]
	if r := h.do("POST", "/v1/works/"+appealable+"/appeal", student, map[string]any{"item": item, "reason": "请再看一下第 3 步。"}, nil); r.code != 200 || r.str("status") != "appealing" {
		t.Errorf("appealing a published grade: %d %v", r.code, r.body)
	}
	if r := h.do("POST", "/v1/works/"+appealable+"/appeal", student, map[string]any{"item": item, "reason": "再申诉一次"}, nil); r.code == 200 {
		t.Errorf("a work can be appealed once only")
	}
	if r := h.do("POST", "/v1/works/"+reviewing+"/appeal", student, map[string]any{"item": item, "reason": "x"}, nil); r.code != 400 {
		t.Errorf("an unpublished work cannot be appealed: %d", r.code)
	}
	if r := h.do("PUT", "/v1/review/"+reviewing, student, map[string]any{"scores": scores}, nil); r.code != 403 {
		t.Errorf("students cannot grade: %d", r.code)
	}
	if u := h.do("GET", "/v1/usage?scope=mine&group=lab", student, nil, nil); u.code != 200 || len(u.list("rows")) == 0 {
		t.Errorf("my usage: %d %v", u.code, u.body)
	}
	if u := h.do("GET", "/v1/overview?scope=platform", student, nil, nil); u.code != 403 {
		t.Errorf("students do not see the platform overview: %d", u.code)
	}
}
