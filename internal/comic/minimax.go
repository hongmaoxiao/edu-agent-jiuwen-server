package comic

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
)

// minimax talks to the MiniMax open platform (docs: platform.minimaxi.com/docs):
// H3 video (POST /v2/video_generation, GET /v2/query/video_generation/{id}),
// H3-Image (POST /v1/images/generations and /v1/images/edits), speech (POST /v1/t2a_v2)
// and voice design (POST /v1/voice_design).
type minimax struct {
	base, key string
}

const mm = "minimax"

func (p *minimax) Run(ctx context.Context, job Job) (Outcome, error) {
	switch job.Kind {
	case KindVideo:
		return p.startVideo(ctx, job)
	case KindImage:
		return p.image(ctx, job)
	case KindSpeech:
		return p.speech(ctx, job)
	case KindVoice:
		return p.voice(ctx, job)
	}
	return Outcome{}, failure("UNSUPPORTED", "这个模型不能做这一步")
}

func (p *minimax) startVideo(ctx context.Context, job Job) (Outcome, error) {
	in := job.Input
	content := []map[string]any{{"type": "text", "text": in.Prompt}}
	ratio := in.Ratio
	if in.FirstFrame != "" {
		uris, err := refImages(ctx, job, []string{in.FirstFrame})
		if err != nil {
			return Outcome{}, err
		}
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": uris[0]}, "role": "first_frame"})
		ratio = "adaptive" // 图生视频的画幅由首帧决定
	}
	if len(in.Refs) > 0 {
		uris, err := refImages(ctx, job, in.Refs)
		if err != nil {
			return Outcome{}, err
		}
		for _, uri := range uris {
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": uri}, "role": "reference_image"})
		}
	}
	body := map[string]any{"model": job.Model.remote, "content": content, "resolution": in.Resolution, "duration": in.Duration, "ratio": ratio}
	var out struct {
		TaskID   string    `json:"task_id"`
		BaseResp *baseResp `json:"base_resp"`
	}
	if err := call(ctx, mm, "POST", p.base+"/v2/video_generation", p.key, body, &out); err != nil {
		return Outcome{}, err
	}
	if err := baseErr(mm, out.BaseResp); err != nil {
		return Outcome{}, err
	}
	if out.TaskID == "" {
		return Outcome{}, failure("PROVIDER_ERROR", "模型没有返回任务编号，请重试")
	}
	return Outcome{Pending: true, ProviderTaskID: out.TaskID}, nil
}

func (p *minimax) Check(ctx context.Context, job Job) (Outcome, error) {
	var out struct {
		Task struct {
			Status  string `json:"status"`
			Content struct {
				URL string `json:"url"`
			} `json:"content"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Usage map[string]any `json:"usage"`
		} `json:"task"`
	}
	if err := call(ctx, mm, "GET", p.base+"/v2/query/video_generation/"+job.ProviderTaskID, p.key, nil, &out); err != nil {
		return Outcome{}, err
	}
	switch out.Task.Status {
	case "succeeded":
		data, err := download(ctx, out.Task.Content.URL, 500<<20)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Files: []Produced{{Data: data, Ext: "mp4", Kind: "video", Meta: map[string]any{"usage": out.Task.Usage}}}}, nil
	case "failed", "cancelled":
		code, message := "", "任务被取消"
		if out.Task.Error != nil {
			code, message = out.Task.Error.Code, out.Task.Error.Message
		}
		return Outcome{}, explain(mm, 200, code, message)
	}
	return Outcome{Pending: true}, nil
}

func (p *minimax) image(ctx context.Context, job Job) (Outcome, error) {
	in := job.Input
	body := map[string]any{"model": job.Model.remote, "prompt": in.Prompt, "n": in.Count, "aspect_ratio": in.Ratio,
		"area": in.Resolution, "output_format": in.Format, "response_format": "url"}
	url := p.base + "/v1/images/generations"
	if len(in.Refs) > 0 {
		uris, err := refImages(ctx, job, in.Refs)
		if err != nil {
			return Outcome{}, err
		}
		var images []map[string]string
		for _, uri := range uris {
			images = append(images, map[string]string{"image_url": uri})
		}
		body["images"] = images
		url = p.base + "/v1/images/edits"
	}
	var out struct {
		Data []struct {
			URL           string `json:"url"`
			Size          string `json:"size"`
			RevisedPrompt string `json:"revised_prompt"`
			Error         *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"data"`
		BaseResp *baseResp `json:"base_resp"`
	}
	if err := call(ctx, mm, "POST", url, p.key, body, &out); err != nil {
		return Outcome{}, err
	}
	if err := baseErr(mm, out.BaseResp); err != nil {
		return Outcome{}, err
	}
	var result Outcome
	ext := in.Format
	if ext == "jpg" {
		ext = "jpeg"
	}
	for _, item := range out.Data {
		if item.Error != nil || item.URL == "" {
			result.Failed++
			if item.Error != nil {
				result.FailedReason = explain(mm, 200, item.Error.Code, item.Error.Message).Message
			}
			continue
		}
		data, err := download(ctx, item.URL, 64<<20)
		if err != nil {
			result.Failed++
			result.FailedReason = err.Error()
			continue
		}
		result.Files = append(result.Files, Produced{Data: data, Ext: ext, Kind: "image", Meta: map[string]any{"revised_prompt": item.RevisedPrompt}})
	}
	if missing := in.Count - len(out.Data); missing > 0 {
		result.Failed += missing
	}
	if len(result.Files) == 0 {
		return Outcome{}, failure("IMAGE_FAILED", firstNonEmpty(result.FailedReason, "这次一张图也没有生成出来，请重试或换模型"))
	}
	return result, nil
}

func (p *minimax) speech(ctx context.Context, job Job) (Outcome, error) {
	in := job.Input
	voice := map[string]any{"voice_id": in.VoiceID, "speed": in.Speed, "vol": 1, "pitch": 0}
	if in.Emotion != "" {
		voice["emotion"] = in.Emotion
	}
	body := map[string]any{"model": job.Model.remote, "text": in.Text, "stream": false, "voice_setting": voice,
		"audio_setting": map[string]any{"sample_rate": 32000, "bitrate": 128000, "format": "mp3", "channel": 1}, "output_format": "hex"}
	var out struct {
		Data *struct {
			Audio string `json:"audio"`
		} `json:"data"`
		BaseResp *baseResp `json:"base_resp"`
	}
	if err := call(ctx, mm, "POST", p.base+"/v1/t2a_v2", p.key, body, &out); err != nil {
		return Outcome{}, err
	}
	if err := baseErr(mm, out.BaseResp); err != nil {
		return Outcome{}, err
	}
	if out.Data == nil || out.Data.Audio == "" {
		return Outcome{}, failure("SPEECH_FAILED", "没有生成出音频，请重试")
	}
	audio, err := hex.DecodeString(out.Data.Audio)
	if err != nil {
		return Outcome{}, failure("SPEECH_FAILED", "音频数据损坏，请重试")
	}
	return Outcome{Files: []Produced{{Data: audio, Ext: "mp3", Kind: "audio"}}}, nil
}

func (p *minimax) voice(ctx context.Context, job Job) (Outcome, error) {
	var out struct {
		VoiceID    string    `json:"voice_id"`
		TrialAudio string    `json:"trial_audio"`
		BaseResp   *baseResp `json:"base_resp"`
	}
	body := map[string]any{"prompt": job.Input.Prompt, "preview_text": job.Input.PreviewText}
	if err := call(ctx, mm, "POST", p.base+"/v1/voice_design", p.key, body, &out); err != nil {
		return Outcome{}, err
	}
	if err := baseErr(mm, out.BaseResp); err != nil {
		return Outcome{}, err
	}
	if out.VoiceID == "" {
		return Outcome{}, failure("VOICE_FAILED", "没有生成出音色，请改一下描述再试")
	}
	result := Outcome{Result: map[string]string{"voice_id": out.VoiceID}}
	if audio, err := hex.DecodeString(out.TrialAudio); err == nil && len(audio) > 0 {
		result.Files = []Produced{{Data: audio, Ext: "mp3", Kind: "audio", Meta: map[string]any{"voice_id": out.VoiceID}}}
	}
	return result, nil
}

// previewVoice synthesizes the sample sentence for a preset voice (cached by the caller, not charged).
func (p *minimax) previewVoice(ctx context.Context, voiceID, text string) ([]byte, error) {
	job := Job{Kind: KindSpeech, Model: &Model{remote: "speech-2.8-turbo"}, Input: Input{Text: text, VoiceID: voiceID, Speed: 1}}
	out, err := p.speech(ctx, job)
	if err != nil {
		return nil, err
	}
	return out.Files[0].Data, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// parallel runs fn n times concurrently and returns the results in order.
func parallel[T any](n int, fn func(i int) (T, error)) ([]T, []error) {
	out, errs := make([]T, n), make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = fn(i)
		}()
	}
	wg.Wait()
	return out, errs
}
