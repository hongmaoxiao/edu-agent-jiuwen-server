package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"time"

	"edu-agent-jiuwen-server/internal/account"
	"edu-agent-jiuwen-server/internal/comic"
)

// WithComic turns on the AI 漫剧 studio; without it every /v1/comic route answers 503.
func (s *Server) WithComic(c *comic.Service) { s.comic = c }

// AI 漫剧工作台：项目、生成任务、媒体、创作点（docs/comic-studio.md）。
func (s *Server) registerComic() {
	on := func(fn func(*http.Request, account.Roles) (any, error)) http.HandlerFunc {
		return s.answer(func(r *http.Request, roles account.Roles) (any, error) {
			if s.comic == nil {
				return nil, errComicOff
			}
			return fn(r, roles)
		})
	}
	s.route("GET /v1/comic/catalog", on(func(r *http.Request, roles account.Roles) (any, error) {
		return s.comic.Catalog(r.Context(), roles)
	}))
	s.route("GET /v1/comic/projects", on(func(r *http.Request, roles account.Roles) (any, error) {
		list, err := s.comic.Projects(r.Context(), roles)
		return map[string]any{"projects": list}, err
	}))
	s.route("POST /v1/comic/projects", comicBody(s, func(w http.ResponseWriter, r *http.Request, roles account.Roles, body *projectBody) (any, int, error) {
		p, err := s.comic.CreateProject(r.Context(), roles, body.Name, body.Doc)
		return p, http.StatusCreated, err
	}))
	s.route("GET /v1/comic/projects/{project}", on(func(r *http.Request, roles account.Roles) (any, error) {
		return s.comic.Project(r.Context(), roles, r.PathValue("project"))
	}))
	s.route("PUT /v1/comic/projects/{project}", comicBody(s, func(w http.ResponseWriter, r *http.Request, roles account.Roles, body *projectBody) (any, int, error) {
		out, err := s.comic.SaveProject(r.Context(), roles, r.PathValue("project"), body.Version, body.Name, body.Doc)
		return out, http.StatusOK, err
	}))
	s.route("DELETE /v1/comic/projects/{project}", on(func(r *http.Request, roles account.Roles) (any, error) {
		return nil, s.comic.DeleteProject(r.Context(), roles, r.PathValue("project"))
	}))
	s.route("POST /v1/comic/projects/{project}/media", s.comicUpload)
	s.route("POST /v1/comic/projects/{project}/tasks", comicBody(s, func(w http.ResponseWriter, r *http.Request, roles account.Roles, body *comic.Submission) (any, int, error) {
		t, err := s.comic.Submit(r.Context(), roles, r.PathValue("project"), *body)
		return t, http.StatusCreated, err
	}))
	s.route("POST /v1/comic/estimate", comicBody(s, func(w http.ResponseWriter, r *http.Request, roles account.Roles, body *comic.Submission) (any, int, error) {
		out, err := s.comic.Estimate(r.Context(), roles, *body)
		return out, http.StatusOK, err
	}))
	s.route("GET /v1/comic/tasks", on(func(r *http.Request, roles account.Roles) (any, error) {
		list, err := s.comic.Tasks(r.Context(), roles, r.URL.Query().Get("project_id"), r.URL.Query().Get("open") == "1")
		return map[string]any{"tasks": list}, err
	}))
	s.route("GET /v1/comic/tasks/{task}", on(func(r *http.Request, roles account.Roles) (any, error) {
		return s.comic.Task(r.Context(), roles, r.PathValue("task"))
	}))
	s.route("POST /v1/comic/tasks/{task}/cancel", on(func(r *http.Request, roles account.Roles) (any, error) {
		return s.comic.Cancel(r.Context(), roles, r.PathValue("task"))
	}))
	s.route("GET /v1/comic/ledger", on(func(r *http.Request, roles account.Roles) (any, error) {
		return s.comic.Ledger(r.Context(), roles)
	}))
	s.route("POST /v1/comic/credits", comicBody(s, func(w http.ResponseWriter, r *http.Request, roles account.Roles, body *struct {
		Login  string `json:"login"`
		Amount int    `json:"amount"`
		Note   string `json:"note"`
	}) (any, int, error) {
		balance, err := s.comic.Grant(r.Context(), roles, body.Login, body.Amount, body.Note)
		return map[string]int{"balance": balance}, http.StatusOK, err
	}))
	s.route("GET /v1/comic/voices/{voice}/preview", on(func(r *http.Request, roles account.Roles) (any, error) {
		url, err := s.comic.VoicePreview(r.Context(), r.PathValue("voice"))
		return map[string]string{"url": url}, err
	}))
	// 媒体和试听用签名链接，<img>/<video>/<audio> 不带令牌也能加载；没有签名或过期一律 403。
	s.route("GET /v1/comic/media/{media}", func(w http.ResponseWriter, r *http.Request) {
		if s.comic == nil {
			fail(w, r, errComicOff)
			return
		}
		q := r.URL.Query()
		path, mime, err := s.comic.MediaFile(r.Context(), r.PathValue("media"), q.Get("e"), q.Get("s"))
		if err != nil {
			fail(w, r, err)
			return
		}
		serveFile(w, r, path, mime, q.Get("download"))
	})
	s.route("GET /v1/comic/previews/{name}", func(w http.ResponseWriter, r *http.Request) {
		if s.comic == nil {
			fail(w, r, errComicOff)
			return
		}
		path, err := s.comic.PreviewFile(r.PathValue("name"), r.URL.Query().Get("e"), r.URL.Query().Get("s"))
		if err != nil {
			fail(w, r, err)
			return
		}
		serveFile(w, r, path, "audio/mpeg", "")
	})
}

type projectBody struct {
	Version int             `json:"version"`
	Name    string          `json:"name"`
	Doc     json.RawMessage `json:"doc"`
}

var errComicOff = &comicOff{}

type comicOff struct{}

func (*comicOff) Error() string { return "comic studio is not enabled" }

// comicBody decodes a JSON body of up to 4 MB (project documents are larger than other requests).
func comicBody[T any](s *Server, fn func(http.ResponseWriter, *http.Request, account.Roles, *T) (any, int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roles, ok := s.roles(w, r)
		if !ok {
			return
		}
		if s.comic == nil {
			fail(w, r, errComicOff)
			return
		}
		var body T
		reader := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
		reader.DisallowUnknownFields()
		if err := reader.Decode(&body); err != nil {
			writeError(w, 400, "INVALID_REQUEST", "请求内容不是有效的 JSON，或含有不认识的字段")
			return
		}
		result, status, err := fn(w, r, roles, &body)
		if err != nil {
			fail(w, r, err)
			return
		}
		writeJSON(w, status, result)
	}
}

// comicUpload takes a raw file body; uploads may be slow, so the read deadline is extended for this request.
func (s *Server) comicUpload(w http.ResponseWriter, r *http.Request) {
	roles, ok := s.roles(w, r)
	if !ok {
		return
	}
	if s.comic == nil {
		fail(w, r, errComicOff)
		return
	}
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Minute))
	m, err := s.comic.Upload(r.Context(), roles, r.PathValue("project"), r.URL.Query().Get("target"), r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, 101<<20))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// serveFile streams a stored file with Range support; download=name asks the browser to save it.
func serveFile(w http.ResponseWriter, r *http.Request, path, mime, download string) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, 404, "NOT_FOUND", "文件不存在")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, 404, "NOT_FOUND", "文件不存在")
		return
	}
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Minute))
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if download != "" {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(download))
	}
	http.ServeContent(w, r, "", info.ModTime(), f)
}
