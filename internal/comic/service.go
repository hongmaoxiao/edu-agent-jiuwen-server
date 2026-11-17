// Package comic is the AI 漫剧 production backend: projects, generation tasks run in the background
// against MiniMax / Volcengine Ark / a text model, stored media, and the 创作点 ledger.
package comic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"edu-agent-jiuwen-server/internal/account"
	"edu-agent-jiuwen-server/internal/platform"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrRejected = account.ErrRejected
	ErrNotFound = account.ErrNotFound
	ErrConflict = account.ErrConflict
	// ErrCredits means the balance does not cover a task's estimate (HTTP 402).
	ErrCredits = errors.New("insufficient credits")
)

// Config comes from EDU_COMIC_* (platform.Config).
type Config struct {
	MediaDir       string
	InitialCredits int
	Simulate       bool
	MiniMaxBase    string
	MiniMaxKey     string
	ArkBase        string
	ArkKey         string
	TextBase       string
	TextKey        string
	TextModel      string
	SubtitleFont   string
}

type Service struct {
	db        *pgxpool.Pool
	cfg       Config
	store     *store
	catalog   *catalog
	providers map[string]Provider
	wake      chan struct{}
	now       func() time.Time
}

func New(db *pgxpool.Pool, cfg Config) (*Service, error) {
	st, err := openStore(cfg.MediaDir)
	if err != nil {
		return nil, err
	}
	s := &Service{db: db, cfg: cfg, store: st, providers: map[string]Provider{}, wake: make(chan struct{}, 1), now: time.Now}
	s.providers["sim"] = sim{}
	s.providers["local"] = localComposer{font: cfg.SubtitleFont}
	if cfg.MiniMaxKey != "" {
		s.providers["minimax"] = &minimax{base: strings.TrimRight(cfg.MiniMaxBase, "/"), key: cfg.MiniMaxKey}
	}
	if cfg.ArkKey != "" {
		s.providers["ark"] = &ark{base: strings.TrimRight(cfg.ArkBase, "/"), key: cfg.ArkKey}
	}
	if cfg.TextKey != "" {
		s.providers["text"] = &textModel{base: strings.TrimRight(cfg.TextBase, "/"), key: cfg.TextKey}
	}
	var models []Model
	for _, m := range defaultCatalog() {
		if m.Simulated && !cfg.Simulate {
			continue
		}
		if m.Provider == "text" && cfg.TextModel != "" {
			m.remote = cfg.TextModel
			if cfg.TextModel != "deepseek-chat" {
				m.Name = cfg.TextModel
			}
		}
		_, m.Available = s.providers[m.Provider]
		if !m.Available {
			m.Unavailable = map[string]string{"minimax": "平台还没有配置 MiniMax 密钥", "ark": "平台还没有配置火山方舟密钥", "text": "平台还没有配置文本模型密钥"}[m.Provider]
		}
		models = append(models, m)
	}
	s.catalog = newCatalog(models)
	return s, nil
}

// ---------- 目录与额度 ----------

type Catalog struct {
	Models   []Model `json:"models"`
	Voices   []Voice `json:"voices"`
	Styles   []Style `json:"styles"`
	Balance  int     `json:"balance"`
	Simulate bool    `json:"simulate"`
}

func (s *Service) Catalog(ctx context.Context, r account.Roles) (Catalog, error) {
	balance, err := s.balance(ctx, s.db, r.AccountID)
	return Catalog{Models: s.catalog.models, Voices: presetVoices, Styles: presetStyles, Balance: balance, Simulate: s.cfg.Simulate}, err
}

// balance grants the starting credits the first time an account opens the studio.
func (s *Service) balance(ctx context.Context, db *pgxpool.Pool, accountID string) (int, error) {
	if s.cfg.InitialCredits > 0 {
		if _, err := db.Exec(ctx, `INSERT INTO comic_ledger(id,account_id,kind,amount,note,created_by)
			SELECT $1,$2,'grant',$3,'初始额度','system' WHERE NOT EXISTS (SELECT 1 FROM comic_ledger WHERE account_id=$2 AND created_by='system')`,
			platform.ID("cl"), accountID, s.cfg.InitialCredits); err != nil {
			return 0, err
		}
	}
	return balanceOf(ctx, db, accountID)
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func balanceOf(ctx context.Context, q rowQuerier, accountID string) (int, error) {
	var b int
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN kind='charge' THEN -amount ELSE amount END),0) FROM comic_ledger WHERE account_id=$1`, accountID).Scan(&b)
	return b, err
}

type LedgerEntry struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Amount      int       `json:"amount"`
	Note        string    `json:"note"`
	TaskID      *string   `json:"task_id"`
	TaskLabel   string    `json:"task_label"`
	ProjectName string    `json:"project_name"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Service) Ledger(ctx context.Context, r account.Roles) (map[string]any, error) {
	balance, err := s.balance(ctx, s.db, r.AccountID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT l.id,l.kind,l.amount,l.note,l.task_id,COALESCE(t.label,''),COALESCE(p.name,''),l.created_at
		FROM comic_ledger l LEFT JOIN comic_tasks t ON t.id=l.task_id LEFT JOIN comic_projects p ON p.id=t.project_id
		WHERE l.account_id=$1 ORDER BY l.created_at DESC, l.id LIMIT 300`, r.AccountID)
	if err != nil {
		return nil, err
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LedgerEntry, error) {
		var e LedgerEntry
		return e, row.Scan(&e.ID, &e.Kind, &e.Amount, &e.Note, &e.TaskID, &e.TaskLabel, &e.ProjectName, &e.CreatedAt)
	})
	return map[string]any{"balance": balance, "entries": entries}, err
}

// Grant adds credits to an account by login name (operators only).
func (s *Service) Grant(ctx context.Context, r account.Roles, login string, amount int, note string) (int, error) {
	if !r.Operator {
		return 0, account.ErrForbidden
	}
	return GrantByLogin(ctx, s.db, login, amount, note, r.LoginAccountID)
}

// GrantByLogin is shared by the operator API and cmd/admin comic-grant.
func GrantByLogin(ctx context.Context, db *pgxpool.Pool, login string, amount int, note, by string) (int, error) {
	if amount <= 0 || amount > 1_000_000 {
		return 0, rejectf("额度要是 1 到 1000000 之间的整数")
	}
	var id string
	if err := db.QueryRow(ctx, "SELECT id FROM accounts WHERE login=$1", strings.ToLower(strings.TrimSpace(login))).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	if _, err := db.Exec(ctx, `INSERT INTO comic_ledger(id,account_id,kind,amount,note,created_by) VALUES ($1,$2,'grant',$3,$4,$5)`,
		platform.ID("cl"), id, amount, firstNonEmpty(note, "加额度"), by); err != nil {
		return 0, err
	}
	return balanceOf(ctx, db, id)
}

// ---------- 项目 ----------

const maxDoc = 2 << 20

type ProjectRow struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Version   int             `json:"version"`
	Summary   json.RawMessage `json:"summary"`
	CoverURL  *string         `json:"cover_url"`
	OpenTasks int             `json:"open_tasks"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type Project struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Version   int             `json:"version"`
	Doc       json.RawMessage `json:"doc"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return name, rejectf("项目名不能为空")
	}
	if utf8.RuneCountInString(name) > 15 {
		return name, rejectf("项目名最多 15 个字")
	}
	return name, nil
}

func checkDoc(doc json.RawMessage) (json.RawMessage, error) {
	if len(doc) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(doc) > maxDoc {
		return nil, rejectf("项目内容太大（超过 2 MB）")
	}
	var probe map[string]any
	if json.Unmarshal(doc, &probe) != nil {
		return nil, rejectf("项目内容必须是 JSON 对象")
	}
	return doc, nil
}

func (s *Service) Projects(ctx context.Context, r account.Roles) ([]ProjectRow, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id,p.name,p.version,COALESCE(p.doc->'summary','null'::jsonb),COALESCE(p.doc->'summary'->>'cover',''),
		(SELECT count(*) FROM comic_tasks t WHERE t.project_id=p.id AND t.status IN ('queued','running')),p.created_at,p.updated_at
		FROM comic_projects p WHERE p.account_id=$1 AND p.deleted_at IS NULL ORDER BY p.updated_at DESC`, r.AccountID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProjectRow, error) {
		var p ProjectRow
		var cover string
		err := row.Scan(&p.ID, &p.Name, &p.Version, &p.Summary, &cover, &p.OpenTasks, &p.CreatedAt, &p.UpdatedAt)
		if cover != "" {
			var owner string
			if s.db.QueryRow(ctx, "SELECT project_id FROM comic_media WHERE id=$1 AND kind='image'", cover).Scan(&owner) == nil && owner == p.ID {
				u := s.mediaURL(cover)
				p.CoverURL = &u
			}
		}
		return p, err
	})
	if list == nil {
		list = []ProjectRow{}
	}
	return list, err
}

func (s *Service) CreateProject(ctx context.Context, r account.Roles, name string, doc json.RawMessage) (Project, error) {
	var p Project
	name, err := checkName(name)
	if err != nil {
		return p, err
	}
	if doc, err = checkDoc(doc); err != nil {
		return p, err
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM comic_projects WHERE account_id=$1 AND deleted_at IS NULL", r.AccountID).Scan(&count); err != nil {
		return p, err
	}
	if count >= 50 {
		return p, rejectf("最多 50 个项目，先删掉不用的")
	}
	p = Project{ID: platform.ID("cp"), Name: name, Version: 1, Doc: doc}
	err = s.db.QueryRow(ctx, `INSERT INTO comic_projects(id,account_id,name,doc) VALUES ($1,$2,$3,$4) RETURNING created_at,updated_at`,
		p.ID, r.AccountID, name, []byte(doc)).Scan(&p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Service) project(ctx context.Context, r account.Roles, id string) (Project, error) {
	var p Project
	err := s.db.QueryRow(ctx, `SELECT id,name,version,doc,created_at,updated_at FROM comic_projects WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`,
		id, r.AccountID).Scan(&p.ID, &p.Name, &p.Version, &p.Doc, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Service) Project(ctx context.Context, r account.Roles, id string) (map[string]any, error) {
	p, err := s.project(ctx, r, id)
	if err != nil {
		return nil, err
	}
	media, err := s.listMedia(ctx, "project_id=$1", id)
	if err != nil {
		return nil, err
	}
	tasks, err := s.listTasks(ctx, "t.project_id=$1", id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": p, "media": media, "tasks": tasks}, nil
}

func (s *Service) SaveProject(ctx context.Context, r account.Roles, id string, version int, name string, doc json.RawMessage) (map[string]any, error) {
	name, err := checkName(name)
	if err != nil {
		return nil, err
	}
	if doc, err = checkDoc(doc); err != nil {
		return nil, err
	}
	var updated time.Time
	var next int
	err = s.db.QueryRow(ctx, `UPDATE comic_projects SET name=$4,doc=$5,version=version+1,updated_at=now()
		WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL AND version=$3 RETURNING version,updated_at`,
		id, r.AccountID, version, name, []byte(doc)).Scan(&next, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := s.project(ctx, r, id); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: 这个项目在别处改过了，请刷新后再改", ErrConflict)
	}
	return map[string]any{"version": next, "updated_at": updated}, err
}

func (s *Service) DeleteProject(ctx context.Context, r account.Roles, id string) error {
	tag, err := s.db.Exec(ctx, "UPDATE comic_projects SET deleted_at=now() WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL", id, r.AccountID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err == nil {
		// 删项目时取消还在排队的任务并退点；执行中的让它跑完，结果仍记在账上。
		_, err = s.db.Exec(ctx, "UPDATE comic_tasks SET next_check_at=now() WHERE project_id=$1 AND status='queued'", id)
	}
	return err
}

// ---------- 媒体 ----------

type Media struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"project_id"`
	TaskID     *string         `json:"task_id"`
	Target     string          `json:"target"`
	Kind       string          `json:"kind"`
	Origin     string          `json:"origin"`
	URL        string          `json:"url"`
	Mime       string          `json:"mime"`
	Bytes      int64           `json:"bytes"`
	Width      int             `json:"width"`
	Height     int             `json:"height"`
	DurationMS int             `json:"duration_ms"`
	Meta       json.RawMessage `json:"meta"`
	CreatedAt  time.Time       `json:"created_at"`
	path       string
}

func (s *Service) mediaURL(id string) string {
	return "/v1/comic/media/" + id + "?" + s.store.sign(id, s.now().Add(12*time.Hour))
}

const mediaColumns = "id,project_id,task_id,target,kind,origin,path,mime,bytes,width,height,duration_ms,meta,created_at"

func (s *Service) listMedia(ctx context.Context, where string, args ...any) ([]Media, error) {
	rows, err := s.db.Query(ctx, "SELECT "+mediaColumns+" FROM comic_media WHERE "+where+" ORDER BY created_at DESC, id LIMIT 2000", args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Media, error) {
		var m Media
		err := row.Scan(&m.ID, &m.ProjectID, &m.TaskID, &m.Target, &m.Kind, &m.Origin, &m.path, &m.Mime, &m.Bytes, &m.Width, &m.Height, &m.DurationMS, &m.Meta, &m.CreatedAt)
		m.URL = s.mediaURL(m.ID)
		return m, err
	})
	if list == nil {
		list = []Media{}
	}
	return list, err
}

var uploadKinds = map[string]struct {
	ext   string
	limit int64
}{
	"image/png": {"png", 15 << 20}, "image/jpeg": {"jpg", 15 << 20}, "image/webp": {"webp", 15 << 20},
	"audio/mpeg": {"mp3", 20 << 20}, "audio/mp3": {"mp3", 20 << 20}, "audio/wav": {"wav", 20 << 20}, "audio/x-wav": {"wav", 20 << 20},
	"video/mp4": {"mp4", 100 << 20},
}

// Upload stores a file the student brings (reference image, cover, BGM).
func (s *Service) Upload(ctx context.Context, r account.Roles, projectID, target, mime string, body io.Reader) (Media, error) {
	if _, err := s.project(ctx, r, projectID); err != nil {
		return Media{}, err
	}
	kind, ok := uploadKinds[strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))]
	if !ok {
		return Media{}, rejectf("只能上传 PNG、JPG、WEBP 图片，MP3、WAV 音频或 MP4 视频")
	}
	if len(target) > 200 {
		return Media{}, rejectf("target 太长")
	}
	data, err := io.ReadAll(io.LimitReader(body, kind.limit+1))
	if err != nil {
		return Media{}, rejectf("文件没有传完整，请重试")
	}
	if int64(len(data)) > kind.limit {
		return Media{}, rejectf("文件太大，最大 %d MB", kind.limit>>20)
	}
	if len(data) == 0 {
		return Media{}, rejectf("文件是空的")
	}
	work, err := os.MkdirTemp("", "comic-upload-")
	if err != nil {
		return Media{}, err
	}
	defer os.RemoveAll(work)
	saved, err := s.save(ctx, r.AccountID, projectID, nil, target, "upload", Produced{Data: data, Ext: kind.ext, Meta: map[string]any{}}, work)
	return saved, err
}

// save writes one produced file to the store, probes it and records it.
func (s *Service) save(ctx context.Context, accountID, projectID string, taskID *string, target, origin string, p Produced, work string) (Media, error) {
	id := platform.ID("cm")
	rel := filepath.ToSlash(filepath.Join(accountID, projectID, id+"."+p.Ext))
	dst := s.store.abs(rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return Media{}, err
	}
	if p.Path != "" {
		data, err := os.ReadFile(p.Path)
		if err != nil {
			return Media{}, err
		}
		p.Data = data
	}
	if err := os.WriteFile(dst, p.Data, 0o640); err != nil {
		return Media{}, err
	}
	m := Media{ID: id, ProjectID: projectID, TaskID: taskID, Target: target, Origin: origin, Mime: mimeOf[p.Ext], Bytes: int64(len(p.Data)), path: rel}
	if p.Kind == "subtitle" {
		m.Kind = "subtitle"
	} else {
		info, err := probe(ctx, dst)
		if err != nil {
			os.Remove(dst)
			return Media{}, err
		}
		m.Kind = kindFromProbe(info, p.Ext)
		if m.Kind == "" || (p.Kind != "" && m.Kind != p.Kind) {
			os.Remove(dst)
			return Media{}, rejectf("文件内容和类型对不上（%s）", p.Ext)
		}
		m.Width, m.Height = info.Width, info.Height
		if m.Kind != "image" {
			m.DurationMS = info.DurationMS
		}
		if p.Meta == nil {
			p.Meta = map[string]any{}
		}
		if m.Kind == "video" {
			p.Meta["has_audio"] = info.HasAudio
		}
	}
	meta, _ := json.Marshal(p.Meta)
	m.Meta = meta
	err := s.db.QueryRow(ctx, `INSERT INTO comic_media(id,account_id,project_id,task_id,target,kind,origin,path,mime,bytes,width,height,duration_ms,meta)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING created_at`,
		id, accountID, projectID, taskID, target, m.Kind, origin, rel, m.Mime, m.Bytes, m.Width, m.Height, m.DurationMS, meta).Scan(&m.CreatedAt)
	if err != nil {
		os.Remove(dst)
		return Media{}, err
	}
	m.URL = s.mediaURL(id)
	return m, nil
}

// MediaFile resolves a signed media URL to a file on disk.
func (s *Service) MediaFile(ctx context.Context, id, e, sig string) (path, mime string, err error) {
	if !s.store.verify(id, e, sig, s.now()) {
		return "", "", account.ErrForbidden
	}
	var rel string
	if err := s.db.QueryRow(ctx, "SELECT path,mime FROM comic_media WHERE id=$1", id).Scan(&rel, &mime); errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	} else if err != nil {
		return "", "", err
	}
	return s.store.abs(rel), mime, nil
}

// VoicePreview returns a signed URL for a preset voice's sample, synthesizing and caching it once.
func (s *Service) VoicePreview(ctx context.Context, voiceID string) (string, error) {
	if !slices.ContainsFunc(presetVoices, func(v Voice) bool { return v.ID == voiceID }) {
		return "", ErrNotFound
	}
	name := "voice-" + strings.NewReplacer(" ", "_", "(", "", ")", "", "/", "_").Replace(voiceID) + ".mp3"
	path := filepath.Join(s.store.dir, "previews", name)
	if _, err := os.Stat(path); err != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return "", err
		}
		var data []byte
		if p, ok := s.providers["minimax"].(*minimax); ok {
			if data, err = p.previewVoice(ctx, voiceID, "你好，这是我的声音。很高兴和你一起把故事讲完。"); err != nil {
				return "", err
			}
		} else if s.cfg.Simulate {
			work, _ := os.MkdirTemp("", "comic-voice-")
			defer os.RemoveAll(work)
			if data, err = simTone(ctx, work, 2.4, 160+float64(hash(voiceID)%240)); err != nil {
				return "", err
			}
		} else {
			return "", rejectf("平台还没有配置 MiniMax 密钥，暂时不能试听")
		}
		if err := os.WriteFile(path, data, 0o640); err != nil {
			return "", err
		}
	}
	id := "preview:" + name
	return "/v1/comic/previews/" + name + "?" + s.store.sign(id, s.now().Add(12*time.Hour)), nil
}

func (s *Service) PreviewFile(name, e, sig string) (string, error) {
	if strings.ContainsAny(name, `/\`) || !strings.HasPrefix(name, "voice-") || !s.store.verify("preview:"+name, e, sig, s.now()) {
		return "", account.ErrForbidden
	}
	return filepath.Join(s.store.dir, "previews", name), nil
}

// UseProvider replaces a provider and marks its models available (tests, and future providers wired at startup).
func (s *Service) UseProvider(name string, p Provider) {
	s.providers[name] = p
	for i := range s.catalog.models {
		if s.catalog.models[i].Provider == name {
			s.catalog.models[i].Available, s.catalog.models[i].Unavailable = true, ""
		}
	}
}
