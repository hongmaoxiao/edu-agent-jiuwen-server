package content

import (
	"context"
	"os"
	"strings"
	"testing"

	"edu-agent-jiuwen-server/internal/testdb"
)

func seed(t *testing.T) Export {
	t.Helper()
	raw, err := os.ReadFile("../../seed/fde-content.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The committed seed must stay importable: every lab has a guide, the tools lab has its 100-point rubric.
func TestSeedIsComplete(t *testing.T) {
	e := seed(t)
	course := e.Courses[0]
	if len(e.Categories) != 2 || len(course.Chapters) != 10 || len(course.Labs) != 51 {
		t.Fatalf("categories %d, chapters %d, labs %d", len(e.Categories), len(course.Chapters), len(course.Labs))
	}
	for _, lab := range course.Labs {
		if len(lab.GuideSteps) < 3 || len(lab.Deliverables) == 0 || lab.Scenario == "" || lab.ChapterSourceID == "" {
			t.Errorf("lab %s is missing its guide, deliverables, scenario or chapter", lab.SourceID)
		}
	}
	var total float64
	for _, item := range e.Rubrics[0].Items {
		total += item.MaxScore
	}
	if len(e.Rubrics) != 1 || e.Rubrics[0].LabSourceID != "lab-workswarm-tools" || total != 100 || len(e.Rubrics[0].Items) != 6 {
		t.Errorf("tools rubric: %d rubrics, %v points", len(e.Rubrics), total)
	}
}

func TestDecodeRejectsBrokenExports(t *testing.T) {
	if _, err := Decode([]byte(`{"categories":[],"courses":[],"rubrics":[],"extra":1}`)); err == nil {
		t.Error("unknown field accepted")
	}
	broken := `{"categories":[{"id":"fde","title":"FDE","status":"live"}],"courses":[{"source_id":"c","category_id":"fde","title":"课",
		"chapters":[],"labs":[{"source_id":"l","chapter_source_id":"missing","title":"实训"}]}],"rubrics":[]}`
	if _, err := Decode([]byte(broken)); err == nil || !strings.Contains(err.Error(), "known chapter") {
		t.Errorf("lab in an unknown chapter: %v", err)
	}
}

func TestImportIsRepeatableAndKeepsLocalEdits(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	e := seed(t)
	first, err := Import(ctx, db, e)
	if err != nil {
		t.Fatal(err)
	}
	if first.Categories.Created != 2 || first.Courses.Created != 1 || first.Chapters.Created != 10 || first.Labs.Created != 51 || first.Rubrics.Created != 1 {
		t.Fatalf("first import: %+v", first)
	}
	var steps, labsWithGuide int
	if err := db.QueryRow(ctx, "SELECT sum(jsonb_array_length(guide_steps)), count(*) FILTER (WHERE jsonb_array_length(guide_steps) > 0) FROM labs").Scan(&steps, &labsWithGuide); err != nil || labsWithGuide != 51 || steps != 255 {
		t.Errorf("guides in the database: %d labs, %d steps, %v", labsWithGuide, steps, err)
	}
	again, err := Import(ctx, db, e)
	if err != nil {
		t.Fatal(err)
	}
	if again.Categories.Unchanged != 2 || again.Courses.Unchanged != 1 || again.Chapters.Unchanged != 10 || again.Labs.Unchanged != 51 || again.Rubrics.Unchanged != 1 {
		t.Fatalf("rerun must change nothing: %+v", again)
	}

	// A teacher edits L01 here; the source later changes L01 and L02. Only L02 follows the source.
	if _, err := db.Exec(ctx, "UPDATE labs SET title='教师改过的标题', edited_at=now() WHERE source_id='lab-fde-01'"); err != nil {
		t.Fatal(err)
	}
	e.Courses[0].Labs[0].Title = "来源改过的标题一"
	e.Courses[0].Labs[1].Title = "来源改过的标题二"
	e.Rubrics[0].Items[0].Conditions += "（补充说明）"
	third, err := Import(ctx, db, e)
	if err != nil {
		t.Fatal(err)
	}
	if third.Labs.KeptEdited != 1 || third.Labs.Updated != 1 || len(third.Kept) != 1 || third.Kept[0] != "lab lab-fde-01" || third.Rubrics.Updated != 1 {
		t.Fatalf("third import: %+v", third)
	}
	var kept, followed string
	db.QueryRow(ctx, "SELECT title FROM labs WHERE source_id='lab-fde-01'").Scan(&kept)
	db.QueryRow(ctx, "SELECT title FROM labs WHERE source_id='lab-fde-02'").Scan(&followed)
	if kept != "教师改过的标题" || followed != "来源改过的标题二" {
		t.Errorf("titles after import: %q, %q", kept, followed)
	}
	var versions int
	var maxScore float64
	if err := db.QueryRow(ctx, "SELECT count(*), max(max_score) FROM rubric_versions").Scan(&versions, &maxScore); err != nil || versions != 2 || maxScore != 100 {
		t.Errorf("rubric versions %d, max score %v, %v", versions, maxScore, err)
	}
}
