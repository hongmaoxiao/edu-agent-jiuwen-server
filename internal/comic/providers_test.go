package comic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAPI records the last request body per path and answers with canned JSON.
type fakeAPI struct {
	bodies  map[string]map[string]any
	answers map[string]string
	files   map[string][]byte
}

func (f *fakeAPI) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := f.files[r.URL.Path]; ok {
			w.Write(data)
			return
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		f.bodies[r.URL.Path] = body
		answer, ok := f.answers[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if strings.HasPrefix(answer, "!") {
			code, rest, _ := strings.Cut(answer[1:], " ")
			w.WriteHeader(map[string]int{"402": 402, "422": 422, "429": 429}[code])
			answer = rest
		}
		w.Write([]byte(strings.ReplaceAll(answer, "$URL", "http://"+r.Host)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testPNG(t *testing.T, dir string) File {
	path := filepath.Join(dir, "ref.png")
	if err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=320x480", "-frames:v", "1", path).Run(); err != nil {
		t.Skip("ffmpeg not installed")
	}
	return File{ID: "cm_ref", Path: path, Kind: "image", Mime: "image/png", Width: 320, Height: 480}
}

func model(id string) *Model {
	for _, m := range defaultCatalog() {
		if m.ID == id {
			return &m
		}
	}
	panic(id)
}

func TestMiniMaxVideoRequestAndPolling(t *testing.T) {
	dir := t.TempDir()
	ref := testPNG(t, dir)
	api := &fakeAPI{bodies: map[string]map[string]any{}, files: map[string][]byte{"/out.mp4": []byte("mp4")}, answers: map[string]string{
		"POST /v2/video_generation":         `{"task_id":"t1"}`,
		"GET /v2/query/video_generation/t1": `{"task":{"status":"succeeded","content":{"url":"$URL/out.mp4"},"usage":{"output_seconds":5}}}`,
	}}
	p := &minimax{base: api.server(t).URL, key: "k"}
	job := Job{Kind: KindVideo, Model: model("minimax-h3"), Work: dir, Files: map[string]File{"cm_ref": ref},
		Input: Input{Prompt: "小林抬头", Duration: 5, Resolution: "768P", Ratio: "9:16", FirstFrame: "cm_ref"}}
	out, err := p.Run(context.Background(), job)
	if err != nil || !out.Pending || out.ProviderTaskID != "t1" {
		t.Fatalf("run: %v %+v", err, out)
	}
	body := api.bodies["/v2/video_generation"]
	content := body["content"].([]any)
	if body["model"] != "MiniMax-H3" || body["resolution"] != "768P" || body["duration"] != float64(5) || body["ratio"] != "adaptive" || len(content) != 2 {
		t.Fatalf("request: %v", body)
	}
	img := content[1].(map[string]any)
	if img["role"] != "first_frame" || !strings.HasPrefix(img["image_url"].(map[string]any)["url"].(string), "data:image/png;base64,") {
		t.Fatalf("first frame: %v", img)
	}
	job.ProviderTaskID = "t1"
	done, err := p.Check(context.Background(), job)
	if err != nil || len(done.Files) != 1 || string(done.Files[0].Data) != "mp4" {
		t.Fatalf("check: %v %+v", err, done)
	}
}

func TestMiniMaxErrorsAreExplained(t *testing.T) {
	api := &fakeAPI{bodies: map[string]map[string]any{}, answers: map[string]string{
		"POST /v2/video_generation":         `!422 {"type":"error","error":{"type":"unprocessable_entity_error","message":"video description contains sensitive content (1026)"}}`,
		"GET /v2/query/video_generation/t2": `{"task":{"status":"failed","error":{"code":"1027","message":"output video failed content safety check"}}}`,
		"POST /v1/t2a_v2":                   `{"data":null,"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`,
	}}
	p := &minimax{base: api.server(t).URL, key: "k"}
	job := Job{Kind: KindVideo, Model: model("minimax-h3"), Work: t.TempDir(), Input: Input{Prompt: "x", Duration: 5, Resolution: "768P", Ratio: "9:16"}}
	if _, err := p.Run(context.Background(), job); err == nil || err.(*Failure).Code != "CONTENT_REJECTED" {
		t.Fatalf("422: %v", err)
	}
	if api.bodies["/v2/video_generation"]["ratio"] != "9:16" {
		t.Fatalf("text-to-video must send an explicit ratio: %v", api.bodies["/v2/video_generation"])
	}
	job.ProviderTaskID = "t2"
	if _, err := p.Check(context.Background(), job); err == nil || err.(*Failure).Code != "CONTENT_REJECTED" {
		t.Fatalf("failed task: %v", err)
	}
	speech := Job{Kind: KindSpeech, Model: model("speech-2.8-hd"), Input: Input{Text: "你好", VoiceID: "female-shaonv", Speed: 1}}
	if _, err := p.Run(context.Background(), speech); err == nil || err.(*Failure).Code != "PROVIDER_BALANCE" {
		t.Fatalf("1008: %v", err)
	}
}

func TestMiniMaxImagePartialAndSpeech(t *testing.T) {
	dir := t.TempDir()
	ref := testPNG(t, dir)
	png, _ := os.ReadFile(ref.Path)
	api := &fakeAPI{bodies: map[string]map[string]any{}, files: map[string][]byte{"/a.png": png}, answers: map[string]string{
		"POST /v1/images/edits": `{"data":[{"url":"$URL/a.png","size":"1024x1024"},{"error":{"code":"1027","message":"output image failed content safety check"}}],"base_resp":{"status_code":0}}`,
		"POST /v1/t2a_v2":       `{"data":{"audio":"4944330400","status":2},"base_resp":{"status_code":0,"status_msg":"success"}}`,
	}}
	p := &minimax{base: api.server(t).URL, key: "k"}
	out, err := p.Run(context.Background(), Job{Kind: KindImage, Model: model("minimax-h3-image"), Work: dir, Files: map[string]File{"cm_ref": ref},
		Input: Input{Prompt: "阿宁", Ratio: "3:4", Resolution: "2K", Count: 2, Format: "png", Refs: []string{"cm_ref"}}})
	if err != nil || len(out.Files) != 1 || out.Failed != 1 || !strings.Contains(out.FailedReason, "安全审核") {
		t.Fatalf("partial: %v %+v", err, out)
	}
	body := api.bodies["/v1/images/edits"]
	if body["model"] != "MiniMax-H3-Image" || body["aspect_ratio"] != "3:4" || body["area"] != "2K" || body["n"] != float64(2) || len(body["images"].([]any)) != 1 {
		t.Fatalf("image request: %v", body)
	}
	sp, err := p.Run(context.Background(), Job{Kind: KindSpeech, Model: model("speech-2.8-hd"), Input: Input{Text: "你好", VoiceID: "female-shaonv", Speed: 1.2, Emotion: "happy"}})
	if err != nil || string(sp.Files[0].Data) != "ID3\x04\x00" {
		t.Fatalf("speech: %v %+v", err, sp)
	}
	vs := api.bodies["/v1/t2a_v2"]["voice_setting"].(map[string]any)
	if vs["voice_id"] != "female-shaonv" || vs["speed"] != 1.2 || vs["emotion"] != "happy" {
		t.Fatalf("voice setting: %v", vs)
	}
}

func TestArkSeedanceAndSeedream(t *testing.T) {
	dir := t.TempDir()
	ref := testPNG(t, dir)
	png, _ := os.ReadFile(ref.Path)
	api := &fakeAPI{bodies: map[string]map[string]any{}, files: map[string][]byte{"/v.mp4": []byte("mp4"), "/i.png": png}, answers: map[string]string{
		"POST /api/v3/contents/generations/tasks":      `{"id":"cgt-1"}`,
		"GET /api/v3/contents/generations/tasks/cgt-1": `{"id":"cgt-1","status":"succeeded","content":{"video_url":"$URL/v.mp4"}}`,
		"GET /api/v3/contents/generations/tasks/cgt-2": `{"id":"cgt-2","status":"running"}`,
		"POST /api/v3/images/generations":              `{"data":[{"url":"$URL/i.png"}]}`,
	}}
	p := &ark{base: api.server(t).URL, key: "k"}
	off := false
	job := Job{Kind: KindVideo, Model: model("seedance-2.0-fast"), Work: dir, Files: map[string]File{"cm_ref": ref},
		Input: Input{Prompt: "纸飞机飞向天空", Duration: 5, Resolution: "720p", Ratio: "9:16", Refs: []string{"cm_ref"}, Audio: &off}}
	out, err := p.Run(context.Background(), job)
	if err != nil || out.ProviderTaskID != "cgt-1" {
		t.Fatalf("run: %v %+v", err, out)
	}
	body := api.bodies["/api/v3/contents/generations/tasks"]
	if body["model"] != "doubao-seedance-2-0-fast-260128" || body["generate_audio"] != false || body["watermark"] != false || body["ratio"] != "9:16" {
		t.Fatalf("seedance request: %v", body)
	}
	if role := body["content"].([]any)[1].(map[string]any)["role"]; role != "reference_image" {
		t.Fatalf("reference role: %v", role)
	}
	job.ProviderTaskID = "cgt-1"
	if done, err := p.Check(context.Background(), job); err != nil || string(done.Files[0].Data) != "mp4" {
		t.Fatalf("check: %v", err)
	}
	job.ProviderTaskID = "cgt-2"
	if pending, err := p.Check(context.Background(), job); err != nil || !pending.Pending {
		t.Fatalf("running: %v %+v", err, pending)
	}
	img, err := p.Run(context.Background(), Job{Kind: KindImage, Model: model("seedream-5.5"), Work: dir, Input: Input{Prompt: "长椅", Ratio: "9:16", Resolution: "2K", Count: 2, Format: "png"}})
	if err != nil || len(img.Files) != 2 {
		t.Fatalf("seedream: %v %+v", err, img)
	}
	if size := api.bodies["/api/v3/images/generations"]["size"]; size != "1536x2736" {
		t.Fatalf("seedream size: %v", size)
	}
}

func TestSimBreakdownOfSampleScript(t *testing.T) {
	b := simBreakdown(Input{Script: "人物：小林，短黑发，蓝色休闲外套；阿宁，黑色短发，绿色针织衫。\n场景：傍晚大学校园，一张木质长椅。\n镜头1，5秒：中景，小林握着白色纸飞机。小林轻声说：“这一次，我想试试。”\n镜头2，5秒：近景，阿宁微笑接过纸飞机，说：“收到了。”"})
	if len(b.Characters) != 2 || len(b.Shots) != 2 || b.Shots[0].ShotSize != "中景" || len(b.Shots[0].Dialogue) != 1 || b.Shots[0].Dialogue[0].Speaker != "小林" {
		t.Fatalf("%+v", b)
	}
	if len(b.Props) != 1 || b.Props[0].Name != "白色纸飞机" || len(b.Shots[1].Props) != 1 || b.Shots[1].Dialogue[0].Speaker != "阿宁" {
		t.Fatalf("props %+v %+v", b.Props, b.Shots[1].Props)
	}
}
