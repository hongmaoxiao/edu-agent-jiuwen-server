// Package content owns categories, courses, chapters, labs, step guides and rubrics.
package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"edu-agent-jiuwen-server/internal/platform"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Export is the import file produced by scripts/export_fde_content.py (seed/fde-content.json).
type Export struct {
	Source     json.RawMessage `json:"source"`
	Categories []Category      `json:"categories"`
	Courses    []Course        `json:"courses"`
	Rubrics    []Rubric        `json:"rubrics"`
}

type Category struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	Eyebrow string          `json:"eyebrow"`
	Summary string          `json:"summary"`
	Status  string          `json:"status"`
	Flow    json.RawMessage `json:"flow"`
}

type Course struct {
	SourceID    string    `json:"source_id"`
	CategoryID  string    `json:"category_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Chapters    []Chapter `json:"chapters"`
	Labs        []Lab     `json:"labs"`
}

type Chapter struct {
	SourceID string `json:"source_id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
}

type GuideStep struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Goal     string   `json:"goal"`
	Hint     string   `json:"hint,omitempty"`
	Checks   []string `json:"checks"`
	Question string   `json:"question,omitempty"`
}

type Lab struct {
	SourceID        string      `json:"source_id"`
	ChapterSourceID string      `json:"chapter_source_id"`
	Title           string      `json:"title"`
	Level           string      `json:"level"`
	DurationMinutes int         `json:"duration_minutes"`
	Description     string      `json:"description"`
	Objectives      []string    `json:"objectives"`
	Scenario        string      `json:"scenario"`
	Deliverables    []string    `json:"deliverables"`
	Boundary        string      `json:"boundary"`
	GuideSteps      []GuideStep `json:"guide_steps"`
}

type RubricItem struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	MaxScore      float64 `json:"max_score"`
	Conditions    string  `json:"conditions"`
	MissingPolicy string  `json:"missing_policy,omitempty"`
	Alternatives  string  `json:"alternatives,omitempty"`
}

type Reference struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type Rubric struct {
	LabSourceID string       `json:"lab_source_id"`
	Title       string       `json:"title"`
	Items       []RubricItem `json:"items"`
	References  []Reference  `json:"references"`
}

// Decode reads an export strictly: unknown fields are an error, so a renamed field cannot silently drop content.
func Decode(raw []byte) (Export, error) {
	var e Export
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return e, fmt.Errorf("import file: %w", err)
	}
	return e, e.validate()
}

func (e Export) validate() error {
	categories := map[string]bool{}
	for _, c := range e.Categories {
		if c.ID == "" || c.Title == "" || categories[c.ID] || (c.Status != "live" && c.Status != "coming_soon" && c.Status != "hidden") {
			return fmt.Errorf("category %q: needs a unique id, a title and a valid status", c.ID)
		}
		categories[c.ID] = true
	}
	labs := map[string]bool{}
	for _, course := range e.Courses {
		if course.SourceID == "" || course.Title == "" || !categories[course.CategoryID] {
			return fmt.Errorf("course %q: needs a source id, a title and a known category", course.SourceID)
		}
		chapters := map[string]bool{}
		for _, chapter := range course.Chapters {
			if chapter.SourceID == "" || chapter.Title == "" || chapters[chapter.SourceID] {
				return fmt.Errorf("chapter %q: needs a unique source id and a title", chapter.SourceID)
			}
			chapters[chapter.SourceID] = true
		}
		for _, lab := range course.Labs {
			if lab.SourceID == "" || lab.Title == "" || labs[lab.SourceID] || (lab.ChapterSourceID != "" && !chapters[lab.ChapterSourceID]) {
				return fmt.Errorf("lab %q: needs a unique source id, a title and a known chapter", lab.SourceID)
			}
			labs[lab.SourceID] = true
			steps := map[string]bool{}
			for _, step := range lab.GuideSteps {
				if step.ID == "" || step.Title == "" || steps[step.ID] || len(step.Checks) == 0 {
					return fmt.Errorf("lab %q step %q: needs a unique id, a title and at least one check", lab.SourceID, step.ID)
				}
				steps[step.ID] = true
			}
		}
	}
	for _, rubric := range e.Rubrics {
		if !labs[rubric.LabSourceID] || rubric.Title == "" || len(rubric.Items) == 0 || len(rubric.Items) > 30 {
			return fmt.Errorf("rubric for %q: needs a known lab, a title and 1–30 items", rubric.LabSourceID)
		}
		items := map[string]bool{}
		for _, item := range rubric.Items {
			if item.ID == "" || items[item.ID] || item.MaxScore <= 0 || item.Conditions == "" {
				return fmt.Errorf("rubric for %q item %q: needs a unique id, a positive max score and conditions", rubric.LabSourceID, item.ID)
			}
			items[item.ID] = true
		}
	}
	return nil
}

// Tally counts what an import did with one kind of row.
type Tally struct {
	Created    int `json:"created"`
	Updated    int `json:"updated"`
	Unchanged  int `json:"unchanged"`
	KeptEdited int `json:"kept_edited"` // edited in this system since the last import; not overwritten
}

type Report struct {
	Categories, Courses, Chapters, Labs, Rubrics Tally
	// Kept lists rows the import left alone because they were edited here; someone should compare them by hand.
	Kept []string
}

func hashOf(values ...any) string {
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (t *Tally) count(outcome string) {
	switch outcome {
	case "created":
		t.Created++
	case "updated":
		t.Updated++
	case "unchanged":
		t.Unchanged++
	case "kept":
		t.KeptEdited++
	}
}

// upsert finds a row by its source, then inserts it, updates it, leaves it, or keeps a local edit.
func upsert(ctx context.Context, tx pgx.Tx, table, where string, args []any, hash string, insert func(id string) error, update func(id string) error) (string, string, error) {
	var id, stored string
	var edited bool
	err := tx.QueryRow(ctx, "SELECT id,imported_hash,edited_at IS NOT NULL FROM "+table+" WHERE "+where, args...).Scan(&id, &stored, &edited)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		id = platform.ID(table[:3])
		return id, "created", insert(id)
	case err != nil:
		return "", "", err
	case edited:
		return id, "kept", nil
	case stored == hash:
		return id, "unchanged", nil
	}
	return id, "updated", update(id)
}

// Import writes an export in one transaction and can run any number of times.
// Rows removed from the export are left in place; rows edited in this system are never overwritten.
func Import(ctx context.Context, db *pgxpool.Pool, e Export) (Report, error) {
	var report Report
	tx, err := db.Begin(ctx)
	if err != nil {
		return report, err
	}
	defer tx.Rollback(ctx)
	for i, c := range e.Categories {
		flow := c.Flow
		if len(flow) == 0 {
			flow = json.RawMessage("[]")
		}
		// xmax = 0 tells a fresh insert from an update; no row back means the category was already the same.
		var inserted bool
		err := tx.QueryRow(ctx, `INSERT INTO categories(id,title,eyebrow,summary,flow,status,position) VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (id) DO UPDATE SET title=$2,eyebrow=$3,summary=$4,flow=$5,status=$6,position=$7
			WHERE (categories.title,categories.eyebrow,categories.summary,categories.flow,categories.status,categories.position) IS DISTINCT FROM ($2,$3,$4,$5::jsonb,$6,$7)
			RETURNING xmax = 0`, c.ID, c.Title, c.Eyebrow, c.Summary, flow, c.Status, i+1).Scan(&inserted)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			report.Categories.count("unchanged")
		case err != nil:
			return report, err
		case inserted:
			report.Categories.count("created")
		default:
			report.Categories.count("updated")
		}
	}
	labIDs := map[string]string{}
	for _, course := range e.Courses {
		hash := hashOf(course.CategoryID, course.Title, course.Description)
		courseID, outcome, err := upsert(ctx, tx, "courses", "source_id=$1", []any{course.SourceID}, hash,
			func(id string) error {
				_, err := tx.Exec(ctx, "INSERT INTO courses(id,source_id,category_id,title,description,imported_hash) VALUES($1,$2,$3,$4,$5,$6)", id, course.SourceID, course.CategoryID, course.Title, course.Description, hash)
				return err
			},
			func(id string) error {
				_, err := tx.Exec(ctx, "UPDATE courses SET category_id=$2,title=$3,description=$4,imported_hash=$5,updated_at=now() WHERE id=$1", id, course.CategoryID, course.Title, course.Description, hash)
				return err
			})
		if err != nil {
			return report, fmt.Errorf("course %s: %w", course.SourceID, err)
		}
		report.Courses.count(outcome)
		report.keep(outcome, "course "+course.SourceID)
		chapterIDs := map[string]string{}
		for i, chapter := range course.Chapters {
			position := i + 1
			hash := hashOf(position, chapter.Title, chapter.Content)
			id, outcome, err := upsert(ctx, tx, "chapters", "course_id=$1 AND source_id=$2", []any{courseID, chapter.SourceID}, hash,
				func(id string) error {
					_, err := tx.Exec(ctx, "INSERT INTO chapters(id,course_id,source_id,position,title,content,imported_hash) VALUES($1,$2,$3,$4,$5,$6,$7)", id, courseID, chapter.SourceID, position, chapter.Title, chapter.Content, hash)
					return err
				},
				func(id string) error {
					_, err := tx.Exec(ctx, "UPDATE chapters SET position=$2,title=$3,content=$4,imported_hash=$5 WHERE id=$1", id, position, chapter.Title, chapter.Content, hash)
					return err
				})
			if err != nil {
				return report, fmt.Errorf("chapter %s: %w", chapter.SourceID, err)
			}
			chapterIDs[chapter.SourceID] = id
			report.Chapters.count(outcome)
			report.keep(outcome, "chapter "+chapter.SourceID)
		}
		for i, lab := range course.Labs {
			position := i + 1
			var chapter any
			if lab.ChapterSourceID != "" {
				chapter = chapterIDs[lab.ChapterSourceID]
			}
			objectives, deliverables, steps := jsonOf(lab.Objectives), jsonOf(lab.Deliverables), jsonOf(lab.GuideSteps)
			hash := hashOf(position, lab.ChapterSourceID, lab.Title, lab.Level, lab.DurationMinutes, lab.Description, lab.Objectives, lab.Scenario, lab.Deliverables, lab.Boundary, lab.GuideSteps)
			fields := []any{chapter, position, lab.Title, lab.Level, lab.DurationMinutes, lab.Description, objectives, lab.Scenario, deliverables, lab.Boundary, steps, hash}
			id, outcome, err := upsert(ctx, tx, "labs", "course_id=$1 AND source_id=$2", []any{courseID, lab.SourceID}, hash,
				func(id string) error {
					_, err := tx.Exec(ctx, `INSERT INTO labs(id,course_id,source_id,chapter_id,position,title,level,duration_minutes,description,objectives,scenario,deliverables,boundary,guide_steps,imported_hash)
						VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, append([]any{id, courseID, lab.SourceID}, fields...)...)
					return err
				},
				func(id string) error {
					_, err := tx.Exec(ctx, `UPDATE labs SET chapter_id=$2,position=$3,title=$4,level=$5,duration_minutes=$6,description=$7,objectives=$8,scenario=$9,
						deliverables=$10,boundary=$11,guide_steps=$12,imported_hash=$13,updated_at=now() WHERE id=$1`, append([]any{id}, fields...)...)
					return err
				})
			if err != nil {
				return report, fmt.Errorf("lab %s: %w", lab.SourceID, err)
			}
			labIDs[lab.SourceID] = id
			report.Labs.count(outcome)
			report.keep(outcome, "lab "+lab.SourceID)
		}
	}
	for _, rubric := range e.Rubrics {
		outcome, err := importRubric(ctx, tx, labIDs[rubric.LabSourceID], rubric)
		if err != nil {
			return report, fmt.Errorf("rubric for %s: %w", rubric.LabSourceID, err)
		}
		report.Rubrics.count(outcome)
		report.keep(outcome, "rubric for "+rubric.LabSourceID)
	}
	return report, tx.Commit(ctx)
}

func (r *Report) keep(outcome, what string) {
	if outcome == "kept" {
		r.Kept = append(r.Kept, what)
	}
}

func jsonOf(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

// importRubric appends a confirmed version when the imported rule differs from the latest one.
// A newer version confirmed by a teacher in this system wins: the import keeps it and reports it.
func importRubric(ctx context.Context, tx pgx.Tx, labID string, rubric Rubric) (string, error) {
	body := jsonOf(map[string]any{"items": rubric.Items, "references": rubric.References})
	hash := hashOf(rubric.Title, rubric.Items, rubric.References)
	var total float64
	for _, item := range rubric.Items {
		total += item.MaxScore
	}
	var rubricID string
	err := tx.QueryRow(ctx, "SELECT id FROM rubrics WHERE lab_id=$1", labID).Scan(&rubricID)
	outcome := "updated"
	if errors.Is(err, pgx.ErrNoRows) {
		rubricID, outcome = platform.ID("rub"), "created"
		if _, err := tx.Exec(ctx, "INSERT INTO rubrics(id,lab_id) VALUES($1,$2)", rubricID, labID); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	var latest int
	var latestHash string
	var teacher bool
	err = tx.QueryRow(ctx, "SELECT version,hash,confirmed_by IS NOT NULL FROM rubric_versions WHERE rubric_id=$1 ORDER BY version DESC LIMIT 1", rubricID).Scan(&latest, &latestHash, &teacher)
	switch {
	case err == nil && latestHash == hash:
		return "unchanged", nil
	case err == nil && teacher:
		return "kept", nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	_, err = tx.Exec(ctx, "INSERT INTO rubric_versions(id,rubric_id,version,title,body,max_score,hash) VALUES($1,$2,$3,$4,$5,$6,$7)",
		platform.ID("rbv"), rubricID, latest+1, rubric.Title, body, total, hash)
	return outcome, err
}
