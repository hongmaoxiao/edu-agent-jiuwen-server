package comic

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// sim is the simulated provider for development and demos without keys. It makes real (small)
// files so every later step — adoption, preview, compose, MP4 download — runs for real.
// A prompt containing 【模拟失败】 fails; 【模拟部分】 makes half of the images fail.
type sim struct{}

const (
	simFail    = "【模拟失败】"
	simPartial = "【模拟部分】"
)

func (sim) Run(ctx context.Context, job Job) (Outcome, error) {
	if strings.Contains(job.Input.Prompt+job.Input.Script+job.Input.Text, simFail) {
		return Outcome{}, failure("SIMULATED_FAILURE", "模拟失败：这是为了演示失败状态。去掉提示词里的「【模拟失败】」再试")
	}
	switch job.Kind {
	case KindParse, KindStoryboard:
		return Outcome{Result: simBreakdown(job.Input)}, nil
	case KindImage:
		return simImages(job)
	case KindVoice:
		data, err := simTone(ctx, job.Work, 2.5, 330)
		if err != nil {
			return Outcome{}, err
		}
		id := "sim-voice-" + strconv.FormatUint(hash(job.Input.Prompt), 36)
		return Outcome{Result: map[string]string{"voice_id": id}, Files: []Produced{{Data: data, Ext: "mp3", Kind: "audio", Meta: map[string]any{"voice_id": id}}}}, nil
	case KindSpeech:
		seconds := math.Max(1.2, float64(utf8.RuneCountInString(job.Input.Text))*0.24/job.Input.Speed)
		data, err := simTone(ctx, job.Work, seconds, 180+float64(hash(job.Input.VoiceID)%200))
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Files: []Produced{{Data: data, Ext: "mp3", Kind: "audio"}}}, nil
	case KindVideo:
		return Outcome{Pending: true, ProviderTaskID: "sim-" + job.TaskID}, nil
	}
	return Outcome{}, failure("UNSUPPORTED", "这个模型不能做这一步")
}

// Check finishes a simulated video: the first frame (or a test pattern) with a slow push-in.
func (sim) Check(ctx context.Context, job Job) (Outcome, error) {
	in := job.Input
	w, h := videoSize(in.Ratio, in.Resolution)
	out := filepath.Join(job.Work, "sim.mp4")
	args := []string{"-v", "error", "-y"}
	frames := in.Duration * 24
	if f, ok := job.Files[in.FirstFrame]; ok && in.FirstFrame != "" {
		args = append(args, "-loop", "1", "-t", strconv.Itoa(in.Duration), "-i", f.Path)
	} else if len(in.Refs) > 0 {
		f := job.Files[in.Refs[0]]
		args = append(args, "-loop", "1", "-t", strconv.Itoa(in.Duration), "-i", f.Path)
	} else {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=24:duration=%d", w, h, in.Duration))
	}
	audio := in.Audio == nil || *in.Audio
	if audio {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=220:sample_rate=44100:duration=%d", in.Duration))
	}
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='min(zoom+0.0012,1.15)':d=%d:x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':s=%dx%d:fps=24,format=yuv420p",
		w*2, h*2, w*2, h*2, frames, w, h)
	args = append(args, "-vf", vf, "-frames:v", strconv.Itoa(frames), "-c:v", "libx264", "-preset", "veryfast", "-crf", "28")
	if audio {
		args = append(args, "-af", "volume=0.15", "-c:a", "aac", "-shortest")
	}
	args = append(args, out)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if msg, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		return Outcome{}, fmt.Errorf("simulated video: %v: %s", err, msg)
	}
	return Outcome{Files: []Produced{{Path: out, Ext: "mp4", Kind: "video"}}}, nil
}

func hash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// videoSize is the frame size for a ratio at a resolution label (480p/720p/768P/1080p/2K).
func videoSize(ratio, resolution string) (int, int) {
	short := map[string]int{"480p": 480, "480P": 480, "720p": 720, "768P": 768, "1080p": 1080, "2K": 1440}[resolution]
	if short == 0 {
		short = 720
	}
	w, h := ratioParts(ratio)
	even := func(v float64) int { return int(math.Round(v/2)) * 2 }
	if w >= h {
		return even(float64(short) * w / h), short
	}
	return short, even(float64(short) * h / w)
}

// simImages draws soft gradient pictures with a diagonal "simulated" hatch in one corner.
func simImages(job Job) (Outcome, error) {
	in := job.Input
	long := map[string]int{"1K": 768, "2K": 1024}[in.Resolution]
	if long == 0 {
		long = 768
	}
	rw, rh := ratioParts(in.Ratio)
	w, h := long, int(float64(long)*rh/rw)
	if rh > rw {
		w, h = int(float64(long)*rw/rh), long
	}
	var out Outcome
	for i := range in.Count {
		if strings.Contains(in.Prompt, simPartial) && i%2 == 1 {
			out.Failed++
			out.FailedReason = "模拟部分失败：这张图被设定为失败"
			continue
		}
		seed := hash(fmt.Sprintf("%s#%d#%d", in.Prompt, i, time.Now().UnixNano()))
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		hue := float64(seed%360) / 360
		c1, c2 := hsl(hue, 0.55, 0.78), hsl(math.Mod(hue+0.12, 1), 0.6, 0.55)
		cx, cy, r := float64(w)*(0.3+float64(seed%40)/100), float64(h)*0.45, float64(min(w, h))*0.22
		for y := range h {
			t := float64(y) / float64(h)
			for x := range w {
				px := mix(c1, c2, t)
				if d := math.Hypot(float64(x)-cx, float64(y)-cy); d < r {
					px = mix(px, color.RGBA{255, 255, 255, 255}, 0.35*(1-d/r))
				}
				if x > w-w/5 && y < h/9 && (x+y)/12%2 == 0 { // 角上的斜条纹：模拟结果的标记
					px = mix(px, color.RGBA{60, 30, 90, 255}, 0.5)
				}
				img.Set(x, y, px)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return Outcome{}, err
		}
		out.Files = append(out.Files, Produced{Data: buf.Bytes(), Ext: "png", Kind: "image"})
	}
	if len(out.Files) == 0 {
		return Outcome{}, failure("IMAGE_FAILED", "模拟：这次一张图也没有生成出来")
	}
	return out, nil
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 255}
}

func hsl(h, s, l float64) color.RGBA {
	f := func(n float64) uint8 {
		k := math.Mod(n+h*12, 12)
		a := s * math.Min(l, 1-l)
		return uint8(255 * (l - a*math.Max(-1, math.Min(math.Min(k-3, 9-k), 1))))
	}
	return color.RGBA{f(0), f(8), f(4), 255}
}

// simTone makes a short tremolo tone standing in for speech.
func simTone(ctx context.Context, work string, seconds, freq float64) ([]byte, error) {
	out := filepath.Join(work, fmt.Sprintf("tone-%d.mp3", time.Now().UnixNano()))
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=%.0f:sample_rate=32000:duration=%.2f", freq, seconds),
		"-af", "tremolo=f=5:d=0.6,volume=0.3", "-c:a", "libmp3lame", "-b:a", "64k", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("simulated audio: %v: %s", err, msg)
	}
	return os.ReadFile(out)
}

var (
	reShot   = regexp.MustCompile(`镜头\s*(\d+)\s*[，,]\s*(\d+)\s*秒\s*[：:]\s*(.+)`)
	reSpeech = regexp.MustCompile(`([\p{Han}A-Za-z]{1,6})(?:轻声|小声|大声|笑着|微笑|低声)?说\s*[：:]\s*[“"「]([^”"」]+)[”"」]`)
	reShotSz = regexp.MustCompile(`^(远景|全景|中景|近景|特写)[，,：:]?`)
)

// simBreakdown splits a script written as 人物：…；场景：…；镜头N，M秒：… lines. Anything else becomes
// one character, one scene and up to three shots, which is enough to walk through the workflow.
func simBreakdown(in Input) Breakdown {
	var b Breakdown
	for _, raw := range strings.Split(in.Script, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "人物"):
			body := strings.TrimLeft(strings.TrimPrefix(line, "人物"), "：: ")
			for _, part := range strings.FieldsFunc(body, func(r rune) bool { return r == '；' || r == ';' }) {
				name, desc, _ := strings.Cut(strings.TrimSpace(part), "，")
				name = strings.TrimRight(name, "。")
				if name == "" {
					continue
				}
				desc = strings.TrimRight(desc, "。")
				b.Characters = append(b.Characters, Asset{Name: name, Prompt: name + "，" + desc + "，全身立绘，正面站姿，纯色背景",
					Fields: map[string]string{"appearance": desc, "outfit": pick(desc, "外套", "衫", "衣", "裙"), "hair": pick(desc, "发")}, Voice: "青年，自然"})
			}
		case strings.HasPrefix(line, "场景"):
			body := strings.TrimRight(strings.TrimLeft(strings.TrimPrefix(line, "场景"), "：: "), "。")
			name, _, _ := strings.Cut(body, "，")
			b.Scenes = append(b.Scenes, Asset{Name: clip(name, 12), Prompt: body + "，空镜，无人物",
				Fields: map[string]string{"location": name, "lighting": pick(body, "光", "阳"), "mood": "治愈"}})
		default:
			if m := reShot.FindStringSubmatch(line); m != nil {
				d, _ := strconv.Atoi(m[2])
				s := Shot{Duration: d, Action: m[3], Dialogue: []Line{}, Characters: []string{}, Props: []string{}, Camera: "固定"}
				if sz := reShotSz.FindStringSubmatch(m[3]); sz != nil {
					s.ShotSize = sz[1]
					s.Action = strings.TrimSpace(strings.TrimPrefix(m[3], sz[0]))
				}
				for _, sp := range reSpeech.FindAllStringSubmatch(m[3], -1) {
					s.Dialogue = append(s.Dialogue, Line{Speaker: sp[1], Text: sp[2]})
				}
				if strings.Contains(m[3], "跟随") {
					s.Camera = "跟"
				}
				s.ImagePrompt = s.ShotSize + "，" + s.Action
				s.VideoPrompt = s.Action
				b.Shots = append(b.Shots, s)
			}
		}
	}
	if len(b.Characters) == 0 {
		b.Characters = []Asset{{Name: "主角", Prompt: "故事的主角，全身立绘", Fields: map[string]string{}}}
	}
	if len(b.Scenes) == 0 {
		b.Scenes = []Asset{{Name: "主场景", Prompt: "故事发生的地点，空镜", Fields: map[string]string{}}}
	}
	for _, word := range []string{"纸飞机", "长椅", "书", "伞", "信", "手机"} {
		if strings.Contains(in.Script, word) && word != "长椅" {
			name := word
			if strings.Contains(in.Script, "白色"+word) {
				name = "白色" + word
			}
			b.Props = append(b.Props, Asset{Name: name, Prompt: name + "，单品，纯色背景", Fields: map[string]string{}})
		}
	}
	if len(b.Shots) == 0 {
		sentences := strings.FieldsFunc(in.Script, func(r rune) bool { return r == '。' || r == '\n' || r == '！' || r == '？' })
		for _, s := range sentences {
			if s = strings.TrimSpace(s); s != "" && len(b.Shots) < 3 {
				b.Shots = append(b.Shots, Shot{Duration: 5, ShotSize: "中景", Camera: "固定", Action: s, Dialogue: []Line{}, ImagePrompt: s, VideoPrompt: s})
			}
		}
	}
	for i := range b.Shots {
		s := &b.Shots[i]
		for _, c := range b.Characters {
			if strings.Contains(s.Action, c.Name) || strings.Contains(s.Action, "两人") {
				s.Characters = append(s.Characters, c.Name)
			}
		}
		s.Scene = b.Scenes[0].Name
		for _, p := range b.Props {
			if strings.Contains(s.Action, strings.TrimPrefix(p.Name, "白色")) {
				s.Props = append(s.Props, p.Name)
			}
		}
	}
	b.normalize()
	return b
}

// pick returns the clause of text that contains one of the words.
func pick(text string, words ...string) string {
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == '，' || r == ',' || r == '、' }) {
		for _, w := range words {
			if strings.Contains(part, w) {
				return strings.TrimSpace(part)
			}
		}
	}
	return ""
}
