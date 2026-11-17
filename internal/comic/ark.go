package comic

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ark talks to Volcengine Ark (docs.volcengine.com/docs/82379): Seedance video
// (POST /api/v3/contents/generations/tasks, GET …/tasks/{id}) and Seedream images (POST /api/v3/images/generations).
type ark struct {
	base, key string
}

const arkName = "ark"

func (p *ark) Run(ctx context.Context, job Job) (Outcome, error) {
	switch job.Kind {
	case KindVideo:
		return p.startVideo(ctx, job)
	case KindImage:
		return p.image(ctx, job)
	}
	return Outcome{}, failure("UNSUPPORTED", "这个模型不能做这一步")
}

func (p *ark) startVideo(ctx context.Context, job Job) (Outcome, error) {
	in := job.Input
	content := []map[string]any{{"type": "text", "text": in.Prompt}}
	if in.FirstFrame != "" {
		uris, err := refImages(ctx, job, []string{in.FirstFrame})
		if err != nil {
			return Outcome{}, err
		}
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": uris[0]}, "role": "first_frame"})
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
	audio := in.Audio == nil || *in.Audio
	body := map[string]any{"model": job.Model.remote, "content": content, "ratio": in.Ratio, "resolution": in.Resolution,
		"duration": in.Duration, "generate_audio": audio, "watermark": false}
	var out struct {
		ID string `json:"id"`
	}
	if err := call(ctx, arkName, "POST", p.base+"/api/v3/contents/generations/tasks", p.key, body, &out); err != nil {
		return Outcome{}, err
	}
	if out.ID == "" {
		return Outcome{}, failure("PROVIDER_ERROR", "模型没有返回任务编号，请重试")
	}
	return Outcome{Pending: true, ProviderTaskID: out.ID}, nil
}

func (p *ark) Check(ctx context.Context, job Job) (Outcome, error) {
	var out struct {
		Status  string `json:"status"`
		Content struct {
			VideoURL string `json:"video_url"`
		} `json:"content"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Usage map[string]any `json:"usage"`
	}
	if err := call(ctx, arkName, "GET", p.base+"/api/v3/contents/generations/tasks/"+job.ProviderTaskID, p.key, nil, &out); err != nil {
		return Outcome{}, err
	}
	switch out.Status {
	case "succeeded":
		data, err := download(ctx, out.Content.VideoURL, 500<<20)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Files: []Produced{{Data: data, Ext: "mp4", Kind: "video", Meta: map[string]any{"usage": out.Usage}}}}, nil
	case "failed", "cancelled", "expired":
		code, message := "", "任务已"+map[string]string{"failed": "失败", "cancelled": "取消", "expired": "过期"}[out.Status]
		if out.Error != nil {
			code, message = out.Error.Code, out.Error.Message
		}
		return Outcome{}, explain(arkName, 200, code, message)
	}
	return Outcome{Pending: true}, nil
}

// seedreamSize turns an area tier and a ratio into explicit pixels (multiples of 16) inside
// Seedream 5.5's allowed range of 921,600 to 4,624,220 pixels.
func seedreamSize(tier, ratio string) string {
	target := map[string]float64{"1K": 1024 * 1024, "2K": 2048 * 2048}[tier]
	if target == 0 {
		target = 2048 * 2048
	}
	target = math.Min(math.Max(target, 921600*1.02), 4624220*0.98)
	w, h := ratioParts(ratio)
	scale := math.Sqrt(target / (w * h))
	round := func(v float64) int { return int(math.Round(v/16)) * 16 }
	return fmt.Sprintf("%dx%d", round(w*scale), round(h*scale))
}

func ratioParts(ratio string) (float64, float64) {
	a, b, ok := strings.Cut(ratio, ":")
	w, err1 := strconv.ParseFloat(a, 64)
	h, err2 := strconv.ParseFloat(b, 64)
	if !ok || err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 1, 1
	}
	return w, h
}

// image asks Seedream once per picture: 5.5 has no multi-image mode, and separate calls let some succeed.
func (p *ark) image(ctx context.Context, job Job) (Outcome, error) {
	in := job.Input
	var refs []string
	if len(in.Refs) > 0 {
		var err error
		if refs, err = refImages(ctx, job, in.Refs); err != nil {
			return Outcome{}, err
		}
	}
	format := in.Format
	if format == "jpg" {
		format = "jpeg"
	}
	results, errs := parallel(in.Count, func(int) ([]byte, error) {
		body := map[string]any{"model": job.Model.remote, "prompt": in.Prompt, "size": seedreamSize(in.Resolution, in.Ratio),
			"response_format": "url", "output_format": format, "watermark": false}
		if len(refs) == 1 {
			body["image"] = refs[0]
		} else if len(refs) > 1 {
			body["image"] = refs
		}
		var out struct {
			Data []struct {
				URL string `json:"url"`
			} `json:"data"`
		}
		if err := call(ctx, arkName, "POST", p.base+"/api/v3/images/generations", p.key, body, &out); err != nil {
			return nil, err
		}
		if len(out.Data) == 0 || out.Data[0].URL == "" {
			return nil, failure("IMAGE_FAILED", "没有生成出图片")
		}
		return download(ctx, out.Data[0].URL, 64<<20)
	})
	var result Outcome
	for i, data := range results {
		if errs[i] != nil {
			result.Failed++
			result.FailedReason = reasonOf(errs[i])
			continue
		}
		result.Files = append(result.Files, Produced{Data: data, Ext: format, Kind: "image"})
	}
	if len(result.Files) == 0 {
		return Outcome{}, firstErr(errs)
	}
	return result, nil
}

func reasonOf(err error) string {
	if f, ok := err.(*Failure); ok {
		return f.Message
	}
	return "生成失败"
}

func firstErr(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
