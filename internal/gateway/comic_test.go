package gateway

import (
	"context"
	"io"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"edu-agent-jiuwen-server/internal/comic"
)

const sampleScript = `《迟到的纸飞机》
人物：小林，短黑发，蓝色休闲外套，内向但认真；阿宁，黑色短发，绿色针织衫，开朗友善。
场景：傍晚大学校园，一张木质长椅，金色阳光，树叶轻晃。
镜头1，5秒：中景，小林坐在长椅左侧，手中握着一架白色纸飞机，犹豫着看向右侧的阿宁。小林轻声说：“这一次，我想试试。”
镜头2，5秒：近景，小林把白色纸飞机轻轻递给阿宁；阿宁微笑接过，说：“你的答案，我收到了。”
镜头3，5秒：全景，两人并肩坐在长椅上，阿宁轻轻放飞纸飞机。镜头跟随纸飞机飞向暖色天空。`

// pricedFake stands in for a paid provider: images fail when the prompt says so, partially otherwise on request.
type pricedFake struct{}

func (pricedFake) Run(ctx context.Context, job comic.Job) (comic.Outcome, error) {
	if strings.Contains(job.Input.Prompt, "fail") {
		return comic.Outcome{}, &comic.Failure{Code: "CONTENT_REJECTED", Message: "内容没有通过模型的安全审核"}
	}
	png, _ := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x64", "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-").Output()
	out := comic.Outcome{Files: []comic.Produced{{Data: png, Ext: "png", Kind: "image"}}}
	if job.Input.Count > 1 {
		out.Failed = job.Input.Count - 1
		out.FailedReason = "模拟失败"
	}
	return out, nil
}

func (pricedFake) Check(context.Context, comic.Job) (comic.Outcome, error) {
	return comic.Outcome{}, nil
}

func comicHarness(t *testing.T) (*harness, *comic.Service, string) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	h := newHarness(t)
	studio, err := comic.New(h.db, comic.Config{MediaDir: t.TempDir(), InitialCredits: 100, Simulate: true})
	if err != nil {
		t.Fatal(err)
	}
	h.s.WithComic(studio)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go studio.Run(ctx)
	return h, studio, h.mustLogin("ops", "operator-password", "desktop")
}

func (h *harness) waitTask(token, id string) reply {
	h.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		r := h.do("GET", "/v1/comic/tasks/"+id, token, nil, nil)
		if s := r.str("status"); s != "queued" && s != "running" {
			return r
		}
		time.Sleep(300 * time.Millisecond)
	}
	h.t.Fatalf("task %s did not finish", id)
	return reply{}
}

func (h *harness) submit(token, project string, body map[string]any) reply {
	h.t.Helper()
	r := h.do("POST", "/v1/comic/projects/"+project+"/tasks", token, body, nil)
	if r.code != 201 {
		h.t.Fatalf("submit %v: %d %v", body["kind"], r.code, r.body)
	}
	return h.waitTask(token, r.str("id"))
}

func TestComicStudioEndToEndWithSimulatedModels(t *testing.T) {
	h, _, token := comicHarness(t)
	cat := h.do("GET", "/v1/comic/catalog", token, nil, nil)
	if cat.code != 200 || cat.body["balance"].(float64) != 100 || cat.body["simulate"] != true {
		t.Fatalf("catalog: %d %v", cat.code, cat.body["balance"])
	}
	p := h.do("POST", "/v1/comic/projects", token, map[string]any{"name": "纸飞机", "doc": map[string]any{"summary": map[string]any{"stage": "script"}}}, nil)
	if p.code != 201 {
		t.Fatalf("create: %d %v", p.code, p.body)
	}
	project := p.str("id")

	parse := h.submit(token, project, map[string]any{"kind": "parse", "model": "sim-text", "target": "project/parse", "label": "解析剧本", "input": map[string]any{"script": sampleScript, "density": "精简"}})
	if parse.str("status") != "succeeded" {
		t.Fatalf("parse: %v", parse.body)
	}
	result := parse.body["result"].(map[string]any)
	if n := len(result["characters"].([]any)); n != 2 {
		t.Fatalf("characters: %d %v", n, result["characters"])
	}
	shots := result["shots"].([]any)
	if len(shots) != 3 || len(shots[0].(map[string]any)["dialogue"].([]any)) != 1 {
		t.Fatalf("shots: %v", shots)
	}

	img := h.submit(token, project, map[string]any{"kind": "image", "model": "sim-image", "target": "asset:a1/form:f1/main", "label": "小林 · 主图",
		"input": map[string]any{"prompt": "小林，全身立绘【模拟部分】", "ratio": "9:16", "resolution": "1K", "count": 4}})
	if img.str("status") != "partial" || len(img.body["media_ids"].([]any)) != 2 {
		t.Fatalf("partial image: %v %v", img.str("status"), img.body["media_ids"])
	}
	frame := img.body["media_ids"].([]any)[0].(string)

	speech := h.submit(token, project, map[string]any{"kind": "speech", "model": "sim-speech", "target": "line:l1/audio", "label": "S01 台词",
		"input": map[string]any{"text": "这一次，我想试试。", "voice_id": "male-qn-daxuesheng", "speed": 1}})
	if speech.str("status") != "succeeded" {
		t.Fatalf("speech: %v", speech.body)
	}
	line := speech.body["media_ids"].([]any)[0].(string)

	video := h.submit(token, project, map[string]any{"kind": "video", "model": "sim-video", "target": "shot:s1/video", "label": "S01 视频",
		"input": map[string]any{"prompt": "小林犹豫地看向阿宁", "duration": 3, "resolution": "480p", "ratio": "9:16", "first_frame": frame}})
	if video.str("status") != "succeeded" {
		t.Fatalf("video: %v", video.body)
	}
	clip := video.body["media_ids"].([]any)[0].(string)

	film := h.submit(token, project, map[string]any{"kind": "compose", "model": "compose", "target": "project/export", "label": "导出成片",
		"input": map[string]any{"compose": map[string]any{"ratio": "9:16", "resolution": "480p", "clips": []any{map[string]any{"media": clip}, map[string]any{"media": clip, "in": 0.5, "hold": 1}},
			"lines": []any{map[string]any{"media": line, "at": 0.5, "text": "这一次，我想试试。"}}, "subtitles": "soft", "transition": "fade", "clean": true}}})
	if film.str("status") != "succeeded" {
		t.Fatalf("compose: %v", film.body)
	}
	manifest := film.body["result"].(map[string]any)["manifest"].(map[string]any)
	if d := manifest["duration_ms"].(float64); d < 6000 || d > 7000 {
		t.Fatalf("film duration %v", d)
	}

	// 勾了清洁版：另存一份不算导出版本的无字幕 MP4。
	cleanID, _ := film.body["result"].(map[string]any)["clean_media"].(string)
	if cleanID == "" {
		t.Fatalf("no clean film: %v", film.body["result"])
	}
	full := h.do("GET", "/v1/comic/projects/"+project, token, nil, nil)
	var mp4 string
	for _, m := range full.body["media"].([]any) {
		m := m.(map[string]any)
		if m["id"] == cleanID && m["target"] != "project/export/clean" {
			t.Fatalf("clean film target %v", m["target"])
		}
		if m["origin"] == "compose" && m["kind"] == "video" && m["target"] == "project/export" {
			mp4 = m["url"].(string)
			if m["width"].(float64) != 480 || m["height"].(float64) != 854 {
				t.Fatalf("film size %vx%v", m["width"], m["height"])
			}
		}
	}
	if mp4 == "" {
		t.Fatal("no exported film in project media")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", mp4, nil)
	r.Header.Set("Range", "bytes=0-99")
	h.s.ServeHTTP(w, r)
	body, _ := io.ReadAll(w.Body)
	if w.Code != 206 || len(body) != 100 || w.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("media fetch: %d %d %q", w.Code, len(body), w.Header().Get("Content-Type"))
	}
	if bad := h.do("GET", strings.Replace(mp4, "s=", "s=0", 1), "", nil, nil); bad.code != 403 {
		t.Fatalf("tampered signature: %d", bad.code)
	}
	// 模拟模型不扣点。
	if l := h.do("GET", "/v1/comic/ledger", token, nil, nil); l.body["balance"].(float64) != 100 {
		t.Fatalf("simulated tasks charged credits: %v", l.body["balance"])
	}
}

func TestComicCreditsChargeRefundAndLimits(t *testing.T) {
	h, studio, token := comicHarness(t)
	studio.UseProvider("minimax", pricedFake{})
	project := h.do("POST", "/v1/comic/projects", token, map[string]any{"name": "额度", "doc": map[string]any{}}, nil).str("id")
	image := func(prompt string, count int) map[string]any {
		return map[string]any{"kind": "image", "model": "minimax-h3-image", "target": "asset:a/form:f/main", "label": "主图",
			"input": map[string]any{"prompt": prompt, "ratio": "1:1", "resolution": "4K", "count": count}}
	}
	if e := h.do("POST", "/v1/comic/estimate", token, image("x", 3), nil); e.body["estimate"].(float64) != 12 {
		t.Fatalf("estimate: %v", e.body)
	}
	// 部分成功：3 张出 1 张，退 2 张的 8 点。
	partial := h.submit(token, project, image("ok", 3))
	if partial.str("status") != "partial" || partial.body["refunded"].(float64) != 8 || partial.body["charged"].(float64) != 4 {
		t.Fatalf("partial: %v", partial.body)
	}
	// 失败：全额退回，原因是人话。
	failed := h.submit(token, project, image("fail", 1))
	if failed.str("status") != "failed" || failed.body["refunded"].(float64) != 4 || !strings.Contains(failed.str("error_message"), "安全审核") {
		t.Fatalf("failed: %v", failed.body)
	}
	if l := h.do("GET", "/v1/comic/ledger", token, nil, nil); l.body["balance"].(float64) != 96 {
		t.Fatalf("balance after partial + failure: %v", l.body["balance"])
	}
	// 同一 request_id 重发（超时重试、连点）：返回同一个任务，只扣一次。
	again := image("ok", 1)
	again["request_id"] = "req-1"
	first := h.do("POST", "/v1/comic/projects/"+project+"/tasks", token, again, nil)
	second := h.do("POST", "/v1/comic/projects/"+project+"/tasks", token, again, nil)
	if first.code != 201 || second.code != 201 || first.str("id") != second.str("id") {
		t.Fatalf("repeated request: %d %v / %d %v", first.code, first.body, second.code, second.body)
	}
	h.waitTask(token, first.str("id"))
	if l := h.do("GET", "/v1/comic/ledger", token, nil, nil); l.body["balance"].(float64) != 92 {
		t.Fatalf("balance after a repeated request: %v", l.body["balance"])
	}
	// 参数随模型：Seedance Fast 不能选 1080p；没配密钥的模型不可用。
	bad := h.do("POST", "/v1/comic/estimate", token, map[string]any{"kind": "video", "model": "minimax-h3-highspeed",
		"input": map[string]any{"prompt": "x", "duration": 5, "resolution": "2K", "ratio": "9:16"}}, nil)
	if bad.code != 400 || !strings.Contains(bad.str("error", "message"), "不支持 2K") {
		t.Fatalf("unsupported resolution: %d %v", bad.code, bad.body)
	}
	if off := h.do("POST", "/v1/comic/estimate", token, map[string]any{"kind": "video", "model": "seedance-2.0-fast",
		"input": map[string]any{"prompt": "x", "duration": 5, "resolution": "720p", "ratio": "9:16"}}, nil); off.code != 400 || !strings.Contains(off.str("error", "message"), "火山方舟") {
		t.Fatalf("unconfigured provider: %d %v", off.code, off.body)
	}
	// 余额不足：402，说明需要多少。
	big := h.do("POST", "/v1/comic/projects/"+project+"/tasks", token, map[string]any{"kind": "video", "model": "minimax-h3",
		"input": map[string]any{"prompt": "x", "duration": 15, "resolution": "2K", "ratio": "9:16"}}, nil)
	if big.code != 402 || !strings.Contains(big.str("error", "message"), "需要 120") {
		t.Fatalf("insufficient credits: %d %v", big.code, big.body)
	}
	// 别人的项目看不到，版本冲突返回 409。
	if r := h.do("PUT", "/v1/comic/projects/"+project, token, map[string]any{"version": 9, "name": "额度", "doc": map[string]any{}}, nil); r.code != 409 {
		t.Fatalf("stale save: %d", r.code)
	}
	if r := h.do("PUT", "/v1/comic/projects/"+project, token, map[string]any{"version": 1, "name": "额度2", "doc": map[string]any{"a": 1}}, nil); r.code != 200 || r.body["version"].(float64) != 2 {
		t.Fatalf("save: %d %v", r.code, r.body)
	}
}
