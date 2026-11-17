package comic

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ComposeSpec is the timeline the client sends. Times are seconds.
type ComposeSpec struct {
	Ratio      string   `json:"ratio"`
	Resolution string   `json:"resolution"`
	FPS        int      `json:"fps,omitempty"`
	Clips      []Clip   `json:"clips"`
	Lines      []TLLine `json:"lines,omitempty"`
	Subtitles  string   `json:"subtitles,omitempty"` // none | soft | burn
	BGM        string   `json:"bgm,omitempty"`
	Volumes    *Volumes `json:"volumes,omitempty"`
	Transition string   `json:"transition,omitempty"` // none | fade
}

type Clip struct {
	Media string  `json:"media"`
	In    float64 `json:"in,omitempty"`
	Out   float64 `json:"out,omitempty"`
	Hold  float64 `json:"hold,omitempty"` // 定格末帧的秒数（配音比镜头长时用，不花点）
	Shot  string  `json:"shot,omitempty"`
}

type TLLine struct {
	Media   string  `json:"media"`
	At      float64 `json:"at"`
	Text    string  `json:"text,omitempty"`
	Speaker string  `json:"speaker,omitempty"`
}

type Volumes struct {
	Native   *float64 `json:"native,omitempty"`
	Dialogue *float64 `json:"dialogue,omitempty"`
	BGM      *float64 `json:"bgm,omitempty"`
}

func (c *ComposeSpec) check() error {
	if len(c.Clips) == 0 {
		return rejectf("时间轴上还没有镜头视频")
	}
	if len(c.Clips) > 60 || len(c.Lines) > 200 {
		return rejectf("镜头或台词太多")
	}
	if !slices.Contains(ratios, c.Ratio) {
		return rejectf("不支持 %s 画幅", c.Ratio)
	}
	if !slices.Contains([]string{"480p", "720p", "1080p"}, c.Resolution) {
		return rejectf("成片分辨率只能是 480p、720p 或 1080p")
	}
	if c.FPS == 0 {
		c.FPS = 24
	}
	if c.FPS != 24 && c.FPS != 30 {
		return rejectf("帧率只能是 24 或 30")
	}
	if c.Subtitles == "" {
		c.Subtitles = "soft"
	}
	if !slices.Contains([]string{"none", "soft", "burn"}, c.Subtitles) {
		return rejectf("字幕方式不对")
	}
	if c.Transition == "" {
		c.Transition = "none"
	}
	if c.Transition != "none" && c.Transition != "fade" {
		return rejectf("转场只能是无或淡入淡出")
	}
	for _, cl := range c.Clips {
		if cl.In < 0 || (cl.Out != 0 && cl.Out <= cl.In+0.2) {
			return rejectf("镜头的裁剪范围不对（出点要比入点晚至少 0.2 秒）")
		}
		if cl.Hold < 0 || cl.Hold > 10 {
			return rejectf("定格末帧最多 10 秒")
		}
	}
	for _, l := range c.Lines {
		if l.At < 0 {
			return rejectf("台词的开始时间不能是负数")
		}
	}
	if c.Volumes == nil {
		c.Volumes = &Volumes{}
	}
	for _, v := range []**float64{&c.Volumes.Native, &c.Volumes.Dialogue, &c.Volumes.BGM} {
		if *v != nil && (**v < 0 || **v > 2) {
			return rejectf("音量要在 0 到 2 之间")
		}
	}
	return nil
}

// mediaIDs lists every file the timeline uses, for ownership checks and loading.
func (c *ComposeSpec) mediaIDs() []string {
	var ids []string
	for _, cl := range c.Clips {
		ids = append(ids, cl.Media)
	}
	for _, l := range c.Lines {
		ids = append(ids, l.Media)
	}
	if c.BGM != "" {
		ids = append(ids, c.BGM)
	}
	return ids
}

func vol(v *float64, fallback float64) float64 {
	if v == nil {
		return fallback
	}
	return *v
}

func secs(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// localComposer renders the timeline with ffmpeg into an H.264/AAC MP4 plus an SRT file.
type localComposer struct {
	font string // CJK font file for burned subtitles; empty means burn falls back to soft
}

func (localComposer) Check(context.Context, Job) (Outcome, error) { return Outcome{}, errNotAsync }

func (lc localComposer) Run(ctx context.Context, job Job) (Outcome, error) {
	spec := job.Input.Compose
	w, h := videoSize(spec.Ratio, spec.Resolution)
	native, dialogue, bgm := vol(spec.Volumes.Native, 1), vol(spec.Volumes.Dialogue, 1), vol(spec.Volumes.BGM, 0.3)
	fade := spec.Transition == "fade"

	var args []string
	var filters []string
	var alabels []string
	total := 0.0
	for i, cl := range spec.Clips {
		f, ok := job.Files[cl.Media]
		if !ok || f.Kind != "video" {
			return Outcome{}, failure("CLIP_MISSING", fmt.Sprintf("第 %d 个镜头的视频找不到了，请重新采用", i+1))
		}
		full := float64(f.DurationMS) / 1000
		out := cl.Out
		if out == 0 || out > full {
			out = full
		}
		if cl.In >= out {
			return Outcome{}, failure("CLIP_RANGE", fmt.Sprintf("第 %d 个镜头的裁剪范围超出了视频长度（%.1f 秒）", i+1, full))
		}
		d := out - cl.In + cl.Hold
		total += d
		args = append(args, "-i", f.Path)
		v := fmt.Sprintf("[%d:v]trim=start=%s:end=%s,setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,fps=%d,format=yuv420p",
			i, secs(cl.In), secs(out), w, h, w, h, spec.FPS)
		if cl.Hold > 0 {
			v += ",tpad=stop_mode=clone:stop_duration=" + secs(cl.Hold)
		}
		if fade && d > 1 {
			v += fmt.Sprintf(",fade=t=in:st=0:d=0.3,fade=t=out:st=%s:d=0.3", secs(d-0.3))
		}
		filters = append(filters, v+fmt.Sprintf("[v%d]", i))
		if f.HasAudio && native > 0 {
			filters = append(filters, fmt.Sprintf("[%d:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aresample=44100,aformat=channel_layouts=stereo,apad=whole_dur=%s,atrim=end=%s,volume=%s[a%d]",
				i, secs(cl.In), secs(out), secs(d), secs(d), secs(native), i))
		} else {
			filters = append(filters, fmt.Sprintf("anullsrc=r=44100:cl=stereo,atrim=end=%s[a%d]", secs(d), i))
		}
		alabels = append(alabels, fmt.Sprintf("[v%d][a%d]", i, i))
	}
	n := len(spec.Clips)
	filters = append(filters, strings.Join(alabels, "")+fmt.Sprintf("concat=n=%d:v=1:a=1[vcat][acat]", n))

	// 台词：每句按开始时间延后，再和原声、配乐混在一起。
	mix := []string{"[acat]"}
	input := n
	for i, l := range spec.Lines {
		f, ok := job.Files[l.Media]
		if !ok || f.Kind != "audio" {
			return Outcome{}, failure("LINE_MISSING", fmt.Sprintf("第 %d 句台词的音频找不到了，请重新采用", i+1))
		}
		args = append(args, "-i", f.Path)
		ms := int(l.At * 1000)
		filters = append(filters, fmt.Sprintf("[%d:a]aresample=44100,aformat=channel_layouts=stereo,adelay=%d|%d,volume=%s[d%d]", input, ms, ms, secs(dialogue), i))
		mix = append(mix, fmt.Sprintf("[d%d]", i))
		input++
	}
	if spec.BGM != "" {
		f, ok := job.Files[spec.BGM]
		if !ok || f.Kind != "audio" {
			return Outcome{}, failure("BGM_MISSING", "配乐文件找不到了，请重新上传")
		}
		args = append(args, "-stream_loop", "-1", "-i", f.Path)
		filters = append(filters, fmt.Sprintf("[%d:a]aresample=44100,aformat=channel_layouts=stereo,atrim=end=%s,afade=t=out:st=%s:d=1.5,volume=%s[bgm]",
			input, secs(total), secs(max(total-1.5, 0)), secs(bgm)))
		mix = append(mix, "[bgm]")
		input++
	}
	audioOut := "[acat]"
	if len(mix) > 1 {
		filters = append(filters, strings.Join(mix, "")+fmt.Sprintf("amix=inputs=%d:duration=first:normalize=0,alimiter=limit=0.95[aout]", len(mix)))
		audioOut = "[aout]"
	}

	// 字幕
	srt := buildSRT(spec.Lines, job.Files)
	srtPath := filepath.Join(job.Work, "film.srt")
	if err := os.WriteFile(srtPath, []byte(srt), 0o600); err != nil {
		return Outcome{}, err
	}
	videoOut := "[vcat]"
	burned := false
	if spec.Subtitles == "burn" && srt != "" && lc.font != "" {
		style := fmt.Sprintf("FontName=%s,FontSize=%d,PrimaryColour=&H00FFFFFF,OutlineColour=&H80000000,BorderStyle=1,Outline=2,Shadow=0,MarginV=%d",
			"Noto Sans CJK SC", max(h/40, 14), h/14)
		filters = append(filters, fmt.Sprintf("[vcat]subtitles=filename='%s':fontsdir='%s':force_style='%s'[vsub]",
			escapeFilter(srtPath), escapeFilter(filepath.Dir(lc.font)), style))
		videoOut = "[vsub]"
		burned = true
	}
	out := filepath.Join(job.Work, "film.mp4")
	cmd := append([]string{"-v", "error", "-y"}, args...)
	soft := spec.Subtitles != "none" && srt != "" && !burned
	if soft {
		cmd = append(cmd, "-i", srtPath)
	}
	cmd = append(cmd, "-filter_complex", strings.Join(filters, ";"), "-map", videoOut, "-map", audioOut)
	if soft {
		cmd = append(cmd, "-map", fmt.Sprintf("%d:s", input), "-c:s", "mov_text", "-metadata:s:s:0", "language=chi")
	}
	cmd = append(cmd, "-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p", "-r", strconv.Itoa(spec.FPS),
		"-c:a", "aac", "-b:a", "160k", "-ar", "44100", "-movflags", "+faststart", "-t", secs(total), out)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if msg, err := exec.CommandContext(ctx, "ffmpeg", cmd...).CombinedOutput(); err != nil {
		return Outcome{}, &Failure{Code: "COMPOSE_FAILED", Message: "合成失败：" + short(lastLine(string(msg))) + "。请检查片段后重试"}
	}
	manifest := map[string]any{"width": w, "height": h, "ratio": spec.Ratio, "resolution": spec.Resolution, "fps": spec.FPS,
		"duration_ms": int(total * 1000), "clips": len(spec.Clips), "lines": len(spec.Lines), "subtitles": map[bool]string{true: "burn", false: spec.Subtitles}[burned],
		"bgm": spec.BGM != "", "transition": spec.Transition}
	if spec.Subtitles == "burn" && !burned {
		manifest["subtitles_note"] = "服务器没有中文字体，字幕改为内嵌字幕轨（播放器里可开关）"
	}
	files := []Produced{{Path: out, Ext: "mp4", Kind: "video", Meta: map[string]any{"manifest": manifest}}}
	if srt != "" {
		files = append(files, Produced{Path: srtPath, Ext: "srt", Kind: "subtitle"})
	}
	return Outcome{Files: files, Result: map[string]any{"manifest": manifest}}, nil
}

func buildSRT(lines []TLLine, files map[string]File) string {
	var b strings.Builder
	n := 0
	stamp := func(s float64) string {
		ms := int(s * 1000)
		return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
	}
	for _, l := range lines {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		d := float64(files[l.Media].DurationMS) / 1000
		if d <= 0 {
			d = 2
		}
		n++
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", n, stamp(l.At), stamp(l.At+d), text)
	}
	return b.String()
}

func escapeFilter(p string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`, `:`, `\:`).Replace(p)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
