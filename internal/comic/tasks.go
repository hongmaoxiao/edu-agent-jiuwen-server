package comic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"edu-agent-jiuwen-server/internal/account"
	"edu-agent-jiuwen-server/internal/platform"

	"github.com/jackc/pgx/v5"
)

type Task struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	ProjectName   string          `json:"project_name"`
	Kind          string          `json:"kind"`
	Target        string          `json:"target"`
	Label         string          `json:"label"`
	Model         string          `json:"model"`
	ModelName     string          `json:"model_name"`
	Provider      string          `json:"provider"`
	Status        string          `json:"status"`
	QueuePosition int             `json:"queue_position"`
	Estimate      int             `json:"estimate"`
	Refunded      int             `json:"refunded"`
	Charged       int             `json:"charged"`
	Input         json.RawMessage `json:"input"`
	Result        json.RawMessage `json:"result"`
	ErrorCode     string          `json:"error_code"`
	ErrorMessage  string          `json:"error_message"`
	Simulated     bool            `json:"simulated"`
	MediaIDs      []string        `json:"media_ids"`
	CreatedAt     time.Time       `json:"created_at"`
	StartedAt     *time.Time      `json:"started_at"`
	FinishedAt    *time.Time      `json:"finished_at"`
}

const taskColumns = `t.id,t.project_id,p.name,t.kind,t.target,t.label,t.model,t.provider,t.status,t.estimate,t.refunded,t.input,
	COALESCE(t.result,'null'::jsonb),t.error_code,t.error_message,t.simulated,t.created_at,t.started_at,t.finished_at,
	COALESCE((SELECT array_agg(m.id ORDER BY m.created_at) FROM comic_media m WHERE m.task_id=t.id),'{}'),
	CASE WHEN t.status='queued' THEN (SELECT count(*) FROM comic_tasks q WHERE q.provider=t.provider AND q.status='queued' AND q.created_at<=t.created_at) ELSE 0 END`

func (s *Service) listTasks(ctx context.Context, where string, args ...any) ([]Task, error) {
	rows, err := s.db.Query(ctx, "SELECT "+taskColumns+" FROM comic_tasks t JOIN comic_projects p ON p.id=t.project_id WHERE "+where+" ORDER BY t.created_at DESC, t.id LIMIT 200", args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Task, error) {
		var t Task
		err := row.Scan(&t.ID, &t.ProjectID, &t.ProjectName, &t.Kind, &t.Target, &t.Label, &t.Model, &t.Provider, &t.Status, &t.Estimate, &t.Refunded, &t.Input,
			&t.Result, &t.ErrorCode, &t.ErrorMessage, &t.Simulated, &t.CreatedAt, &t.StartedAt, &t.FinishedAt, &t.MediaIDs, &t.QueuePosition)
		t.Charged = t.Estimate - t.Refunded
		if m := s.catalog.byID[t.Model]; m != nil {
			t.ModelName = m.Name
		} else {
			t.ModelName = t.Model
		}
		return t, err
	})
	if list == nil {
		list = []Task{}
	}
	return list, err
}

func (s *Service) Tasks(ctx context.Context, r account.Roles, projectID string, open bool) ([]Task, error) {
	where, args := "t.account_id=$1", []any{r.AccountID}
	if projectID != "" {
		where += " AND t.project_id=$2"
		args = append(args, projectID)
	}
	if open {
		where += " AND t.status IN ('queued','running')"
	}
	return s.listTasks(ctx, where, args...)
}

func (s *Service) Task(ctx context.Context, r account.Roles, id string) (Task, error) {
	list, err := s.listTasks(ctx, "t.id=$1 AND t.account_id=$2", id, r.AccountID)
	if err == nil && len(list) == 0 {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return list[0], nil
}

// Submission is one request to run a task.
type Submission struct {
	Kind   string `json:"kind"`
	Model  string `json:"model"`
	Target string `json:"target"`
	Label  string `json:"label"`
	Input  Input  `json:"input"`
}

func (s *Service) prepare(sub *Submission) (*Model, int, error) {
	if _, ok := kindOf[sub.Kind]; !ok {
		return nil, 0, rejectf("不认识的任务类型 %q", sub.Kind)
	}
	m := s.catalog.byID[sub.Model]
	if m == nil {
		return nil, 0, rejectf("不认识的模型 %q", sub.Model)
	}
	if err := check(sub.Kind, m, &sub.Input); err != nil {
		return nil, 0, err
	}
	return m, estimate(m, sub.Input), nil
}

func (s *Service) Estimate(ctx context.Context, r account.Roles, sub Submission) (map[string]int, error) {
	_, cost, err := s.prepare(&sub)
	if err != nil {
		return nil, err
	}
	balance, err := s.balance(ctx, s.db, r.AccountID)
	return map[string]int{"estimate": cost, "balance": balance}, err
}

// Submit validates, charges the estimate and queues the task; the worker picks it up.
func (s *Service) Submit(ctx context.Context, r account.Roles, projectID string, sub Submission) (Task, error) {
	m, cost, err := s.prepare(&sub)
	if err != nil {
		return Task{}, err
	}
	if _, err := s.project(ctx, r, projectID); err != nil {
		return Task{}, err
	}
	if len(sub.Target) > 200 || len([]rune(sub.Label)) > 80 {
		return Task{}, rejectf("target 或 label 太长")
	}
	// 引用的媒体必须属于这个项目。
	refs := append(slices.Clone(sub.Input.Refs), sub.Input.FirstFrame)
	if sub.Input.Compose != nil {
		refs = append(refs, sub.Input.Compose.mediaIDs()...)
	}
	for _, id := range refs {
		if id == "" {
			continue
		}
		var owner string
		if err := s.db.QueryRow(ctx, "SELECT project_id FROM comic_media WHERE id=$1", id).Scan(&owner); err != nil || owner != projectID {
			return Task{}, rejectf("引用的素材不在这个项目里，请重新选择")
		}
	}
	if _, err := s.balance(ctx, s.db, r.AccountID); err != nil {
		return Task{}, err
	}
	input, _ := json.Marshal(sub.Input)
	id := platform.ID("ct")
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('comic-credits:' || $1))", r.AccountID); err != nil {
			return err
		}
		balance, err := balanceOf(ctx, tx, r.AccountID)
		if err != nil {
			return err
		}
		if cost > balance {
			return fmt.Errorf("%w: 创作点不足：这次需要 %d，余额 %d。请找老师加额度", ErrCredits, cost, balance)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO comic_tasks(id,account_id,project_id,kind,target,label,provider,model,input,estimate,simulated)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, r.AccountID, projectID, sub.Kind, sub.Target, strings.TrimSpace(sub.Label), m.Provider, m.ID, input, cost, m.Simulated); err != nil {
			return err
		}
		if cost > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO comic_ledger(id,account_id,task_id,kind,amount,note) VALUES ($1,$2,$3,'charge',$4,$5)`,
				platform.ID("cl"), r.AccountID, id, cost, "提交："+firstNonEmpty(strings.TrimSpace(sub.Label), m.Name))
		}
		return err
	})
	if err != nil {
		return Task{}, err
	}
	s.poke()
	return s.Task(ctx, r, id)
}

// Cancel stops a task that has not reached the provider yet and refunds it.
func (s *Service) Cancel(ctx context.Context, r account.Roles, id string) (Task, error) {
	t, err := s.Task(ctx, r, id)
	if err != nil {
		return t, err
	}
	if t.Status != "queued" {
		return t, rejectf("只能取消还在排队的任务；已经交给模型的任务没法撤回")
	}
	if err := s.finish(ctx, id, "cancelled", nil, t.Estimate, "", "已取消，创作点已退回", "取消退回"); err != nil {
		return t, err
	}
	return s.Task(ctx, r, id)
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ---------- 后台执行 ----------

// Concurrent tasks per provider. Ark's personal accounts allow 3 concurrent Seedance jobs.
var providerSlots = map[string]int{"minimax": 3, "ark": 3, "text": 4, "sim": 4, "local": 1}

// pollEvery is how often an asynchronous provider job is checked.
var pollEvery = map[string]time.Duration{"minimax": 10 * time.Second, "ark": 10 * time.Second, "sim": 2 * time.Second}

// maxRunning fails an asynchronous job that has not finished (providers keep results 24 h–7 days).
const maxRunning = 2 * time.Hour

type claimed struct {
	id, accountID, projectID, kind, target, provider, model, status, providerTaskID string
	input                                                                           json.RawMessage
	estimate                                                                        int
	startedAt                                                                       *time.Time
}

// Run executes queued tasks and polls running ones until ctx ends. Safe to run in several processes:
// each step leases its task row.
func (s *Service) Run(ctx context.Context) {
	var mu sync.Mutex
	busy := map[string]int{}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		for {
			mu.Lock()
			var full []string
			for p, n := range busy {
				if n >= providerSlots[p] {
					full = append(full, p)
				}
			}
			mu.Unlock()
			c, ok, err := s.claim(ctx, full)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("comic task claim failed", "service", "edu-agent-jiuwen-server", "error", err.Error())
				}
				break
			}
			if !ok {
				break
			}
			mu.Lock()
			busy[c.provider]++
			mu.Unlock()
			go func() {
				defer func() {
					mu.Lock()
					busy[c.provider]--
					mu.Unlock()
					s.poke()
				}()
				s.step(ctx, c)
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.wake:
		}
	}
}

// claim leases one due task. Queued tasks only start while their provider has fewer than its
// limit running at the provider (asynchronous jobs count as running until they finish).
func (s *Service) claim(ctx context.Context, full []string) (claimed, bool, error) {
	var c claimed
	if full == nil {
		full = []string{}
	}
	err := s.db.QueryRow(ctx, `UPDATE comic_tasks SET lease_until=now()+interval '15 minutes'
		WHERE id=(SELECT t.id FROM comic_tasks t
			WHERE t.status IN ('queued','running') AND t.next_check_at<=now() AND (t.lease_until IS NULL OR t.lease_until<now())
			AND NOT (t.provider = ANY($1))
			AND (t.status='running' OR (SELECT count(*) FROM comic_tasks r WHERE r.provider=t.provider AND r.status='running') < COALESCE(($2::jsonb->>t.provider)::int, 2))
			ORDER BY t.status DESC, t.created_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id,account_id,project_id,kind,target,provider,model,status,provider_task_id,input,estimate,started_at`, full, slotsJSON()).
		Scan(&c.id, &c.accountID, &c.projectID, &c.kind, &c.target, &c.provider, &c.model, &c.status, &c.providerTaskID, &c.input, &c.estimate, &c.startedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

func slotsJSON() string {
	raw, _ := json.Marshal(providerSlots)
	return string(raw)
}

func (s *Service) step(ctx context.Context, c claimed) {
	fail := func(f *Failure) {
		if err := s.finish(ctx, c.id, "failed", nil, c.estimate, f.Code, f.Message, "失败退回"); err != nil {
			slog.Error("comic task finish failed", "service", "edu-agent-jiuwen-server", "error", err.Error())
		}
	}
	m := s.catalog.byID[c.model]
	p, ok := s.providers[c.provider]
	if m == nil || !ok {
		fail(failure("MODEL_GONE", "这个模型现在不可用了，创作点已退回。请换一个模型重试"))
		return
	}
	var deleted bool
	if err := s.db.QueryRow(ctx, "SELECT deleted_at IS NOT NULL FROM comic_projects WHERE id=$1", c.projectID).Scan(&deleted); err == nil && deleted && c.status == "queued" {
		s.finish(ctx, c.id, "cancelled", nil, c.estimate, "", "项目已删除，任务取消，创作点已退回", "取消退回")
		return
	}
	var in Input
	json.Unmarshal(c.input, &in)
	work, err := os.MkdirTemp("", "comic-task-")
	if err != nil {
		fail(failure("INTERNAL", "服务器临时目录不可用，创作点已退回，请重试"))
		return
	}
	defer os.RemoveAll(work)
	job := Job{TaskID: c.id, Kind: c.kind, Model: m, Input: in, ProviderTaskID: c.providerTaskID, Work: work, Files: map[string]File{}}
	if err := s.loadFiles(ctx, &job); err != nil {
		fail(failure("REF_MISSING", "引用的素材找不到了，创作点已退回。请重新选择后再试"))
		return
	}
	var out Outcome
	switch {
	case c.status == "queued":
		if _, err := s.db.Exec(ctx, "UPDATE comic_tasks SET status='running',started_at=now() WHERE id=$1", c.id); err != nil {
			return
		}
		out, err = p.Run(ctx, job)
	case c.providerTaskID != "":
		if c.startedAt != nil && s.now().Sub(*c.startedAt) > maxRunning {
			fail(failure("PROVIDER_TIMEOUT", "模型两个小时还没出结果，已停止等待，创作点已退回。请重试或换模型"))
			return
		}
		out, err = p.Check(ctx, job)
	default:
		// 同步任务执行到一半服务重启了：结果拿不回来，按失败退点。
		fail(failure("INTERRUPTED", "生成过程中服务重启，结果没能保存，创作点已退回。请重试"))
		return
	}
	if err != nil {
		if ctx.Err() != nil {
			return // 服务正在关闭：租约到期后由下一次启动接着处理
		}
		var f *Failure
		if !errors.As(err, &f) {
			slog.Error("comic task step failed", "service", "edu-agent-jiuwen-server", "kind", c.kind, "provider", c.provider, "error", err.Error())
			f = failure("INTERNAL", "处理结果时出错，创作点已退回，请重试")
		}
		fail(f)
		return
	}
	if out.Pending {
		next := pollEvery[c.provider]
		if next == 0 {
			next = 10 * time.Second
		}
		ptid := firstNonEmpty(out.ProviderTaskID, c.providerTaskID)
		s.db.Exec(ctx, "UPDATE comic_tasks SET provider_task_id=$2,next_check_at=now()+$3::interval,lease_until=NULL WHERE id=$1",
			c.id, ptid, fmt.Sprintf("%d milliseconds", next.Milliseconds()))
		return
	}
	s.complete(ctx, c, m, job, out)
}

// loadFiles resolves the media a job refers to.
func (s *Service) loadFiles(ctx context.Context, job *Job) error {
	ids := append(slices.Clone(job.Input.Refs), job.Input.FirstFrame)
	if job.Input.Compose != nil {
		ids = append(ids, job.Input.Compose.mediaIDs()...)
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		var f File
		var rel string
		var meta map[string]any
		if err := s.db.QueryRow(ctx, "SELECT id,path,kind,mime,duration_ms,width,height,meta FROM comic_media WHERE id=$1", id).
			Scan(&f.ID, &rel, &f.Kind, &f.Mime, &f.DurationMS, &f.Width, &f.Height, &meta); err != nil {
			return err
		}
		f.Path = s.store.abs(rel)
		f.HasAudio, _ = meta["has_audio"].(bool)
		job.Files[id] = f
	}
	return nil
}

func (s *Service) complete(ctx context.Context, c claimed, m *Model, job Job, out Outcome) {
	status, refund, code, message := "succeeded", 0, "", ""
	if out.Failed > 0 {
		status = "partial"
		if per := m.Price.PerImage[job.Input.Resolution]; per > 0 && !m.Simulated {
			refund = min(per*out.Failed, c.estimate)
		}
		code = "PARTIAL"
		message = fmt.Sprintf("%d 张里有 %d 张没生成出来（%s）", out.Failed+len(out.Files), out.Failed, firstNonEmpty(out.FailedReason, "模型未返回"))
		if refund > 0 {
			message += fmt.Sprintf("，已退回 %d 点", refund)
		}
	}
	taskID := c.id
	var saved []Media
	origin := map[bool]string{true: "compose", false: "generated"}[c.kind == KindCompose]
	for _, p := range out.Files {
		if p.Meta == nil {
			p.Meta = map[string]any{}
		}
		p.Meta["model"] = m.ID
		if m.Simulated {
			p.Meta["simulated"] = true
		}
		md, err := s.save(ctx, c.accountID, c.projectID, &taskID, c.target, origin, p, job.Work)
		if err != nil {
			slog.Error("comic media save failed", "service", "edu-agent-jiuwen-server", "kind", c.kind, "error", err.Error())
			for _, done := range saved {
				s.db.Exec(ctx, "DELETE FROM comic_media WHERE id=$1", done.ID)
			}
			s.finish(ctx, c.id, "failed", nil, c.estimate, "SAVE_FAILED", "结果保存失败，创作点已退回，请重试", "失败退回")
			return
		}
		saved = append(saved, md)
	}
	result := out.Result
	if c.kind == KindCompose {
		r := map[string]any{"manifest": nil, "srt_media": nil}
		if mm, ok := out.Result.(map[string]any); ok {
			r["manifest"] = mm["manifest"]
		}
		for _, md := range saved {
			if md.Kind == "subtitle" {
				r["srt_media"] = md.ID
			}
		}
		result = r
	}
	var raw []byte
	if result != nil {
		raw, _ = json.Marshal(result)
	}
	if err := s.finish(ctx, c.id, status, raw, refund, code, message, "部分失败退回"); err != nil {
		slog.Error("comic task finish failed", "service", "edu-agent-jiuwen-server", "error", err.Error())
	}
}

// finish records the end state and any refund in one transaction, exactly once.
func (s *Service) finish(ctx context.Context, id, status string, result []byte, refund int, code, message, note string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var accountID, label string
		var estimate int
		err := tx.QueryRow(ctx, `UPDATE comic_tasks SET status=$2,result=$3,refunded=LEAST($4,estimate),error_code=$5,error_message=$6,finished_at=now(),lease_until=NULL
			WHERE id=$1 AND status IN ('queued','running') RETURNING account_id,label,estimate`, id, status, result, refund, code, message).Scan(&accountID, &label, &estimate)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if amount := min(refund, estimate); amount > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO comic_ledger(id,account_id,task_id,kind,amount,note) VALUES ($1,$2,$3,'refund',$4,$5)`,
				platform.ID("cl"), accountID, id, amount, note+"："+label)
		}
		return err
	})
}
