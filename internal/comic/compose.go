package comic

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ComposeSpec is the timeline the client sends. Times are seconds.
// Two forms: the V2 multi-track Tracks, or the older shot list (Clips, Lines, BGM), which is converted to tracks.
type ComposeSpec struct {
	Ratio      string   `json:"ratio"`
	Resolution string   `json:"resolution"`
	FPS        int      `json:"fps,omitempty"`
	Tracks     *Tracks  `json:"tracks,omitempty"`
	Clips      []Clip   `json:"clips,omitempty"`
	Lines      []TLLine `json:"lines,omitempty"`
	Subtitles  string   `json:"subtitles,omitempty"` // none | soft | burn
	BGM        string   `json:"bgm,omitempty"`
	Volumes    *Volumes `json:"volumes,omitempty"`
	Transition string   `json:"transition,omitempty"` // none | fade
	Clean      bool     `json:"clean,omitempty"`      // 有字幕时另出一份无字幕的清洁版，同一时间轴和声音
	Loudness   float64  `json:"loudness,omitempty"`   // 交付预设的目标响度（LUFS，-24 到 -10）；0 只测量不调整
}

// Tracks is the V2 multi-track timeline (剪辑台). Video clips sit at their start time without overlapping;
// gaps between them render as black with silence. Audio clips mix by role.
type Tracks struct {
	Video     []TrackClip `json:"video"`
	Audio     []TrackClip `json:"audio,omitempty"`
	Subtitles []Subtitle  `json:"subtitles,omitempty"`
	Duck      float64     `json:"duck,omitempty"` // 对白、旁白期间配乐压低的分贝数（负数）；0 不压
}

type TrackClip struct {
	Media   string   `json:"media"`
	Role    string   `json:"role,omitempty"` // 音频：dialogue | narration | music | ambience | sfx
	Start   float64  `json:"start"`
	In      float64  `json:"in,omitempty"`
	Out     float64  `json:"out,omitempty"`    // 0 表示到素材结尾
	Hold    float64  `json:"hold,omitempty"`   // 视频：定格末帧的秒数
	Volume  *float64 `json:"volume,omitempty"` // 视频片段指原生声音，音频片段指它自己；默认 1
	FadeIn  float64  `json:"fade_in,omitempty"`
	FadeOut float64  `json:"fade_out,omitempty"`
	Loop    bool     `json:"loop,omitempty"` // 音频循环播放到成片结尾（配乐）
	Fit     string   `json:"fit,omitempty"`  // 视频画幅和成片不同时：pad（默认，缩小留黑边）| crop（放大裁掉多出的部分）
}

type Subtitle struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
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

var audioRoles = []string{"dialogue", "narration", "music", "ambience", "sfx"}

func (c *ComposeSpec) check() error {
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
	if c.Loudness != 0 && (c.Loudness < -24 || c.Loudness > -10) {
		return rejectf("目标响度要在 -24 到 -10 LUFS 之间")
	}
	if c.Volumes == nil {
		c.Volumes = &Volumes{}
	}
	for _, v := range []**float64{&c.Volumes.Native, &c.Volumes.Dialogue, &c.Volumes.BGM} {
		if *v != nil && (**v < 0 || **v > 2) {
			return rejectf("音量要在 0 到 2 之间")
		}
	}
	if c.Tracks != nil {
		return c.Tracks.check()
	}
	if len(c.Clips) == 0 {
		return rejectf("时间轴上还没有镜头视频")
	}
	if len(c.Clips) > 60 || len(c.Lines) > 200 {
		return rejectf("镜头或台词太多")
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
	return nil
}

func (t *Tracks) check() error {
	if len(t.Video) == 0 {
		return rejectf("时间线上还没有视频片段")
	}
	if len(t.Video) > 120 || len(t.Audio) > 300 || len(t.Subtitles) > 300 {
		return rejectf("片段太多：视频最多 120 段，声音最多 300 段，字幕最多 300 条")
	}
	if t.Duck < -40 || t.Duck > 0 {
		return rejectf("配乐压低要在 -40 到 0 dB 之间")
	}
	clip := func(c TrackClip, what string, min float64) error {
		switch {
		case c.Media == "":
			return rejectf("%s片段没有素材", what)
		case c.Start < 0 || c.In < 0:
			return rejectf("%s片段的时间不能是负数", what)
		case c.Out != 0 && c.Out < c.In+min:
			return rejectf("%s片段的出点要比入点晚至少 %.1f 秒", what, min)
		case c.Hold < 0 || c.Hold > 10:
			return rejectf("定格末帧最多 10 秒")
		case c.FadeIn < 0 || c.FadeOut < 0 || c.FadeIn > 10 || c.FadeOut > 10:
			return rejectf("淡入淡出要在 0 到 10 秒之间")
		case c.Volume != nil && (*c.Volume < 0 || *c.Volume > 2):
			return rejectf("音量要在 0 到 2 之间")
		case c.Fit != "" && c.Fit != "pad" && c.Fit != "crop":
			return rejectf("画幅适配只能是留边（pad）或裁剪（crop）")
		case c.Loop && c.Role != "music" && c.Role != "ambience":
			// 规格 V2：不能悄悄循环素材来填缺口，只有音乐和环境声可以铺满。
			return rejectf("只有音乐和环境声可以循环，%s片段不能循环；长度不够请缩短、定帧或换素材", what)
		}
		return nil
	}
	for _, c := range t.Video {
		if err := clip(c, "视频", 0.2); err != nil {
			return err
		}
	}
	for _, c := range t.Audio {
		if !slices.Contains(audioRoles, c.Role) {
			return rejectf("声音片段的轨道不对：%s", c.Role)
		}
		if err := clip(c, "声音", 0.05); err != nil {
			return err
		}
	}
	for _, s := range t.Subtitles {
		if s.Start < 0 || s.End <= s.Start {
			return rejectf("字幕的结束时间要晚于开始时间")
		}
		if utf8.RuneCountInString(s.Text) > 200 {
			return rejectf("一条字幕最多 200 字")
		}
	}
	return nil
}

// mediaIDs lists every file the timeline uses, for ownership checks and loading.
func (c *ComposeSpec) mediaIDs() []string {
	var ids []string
	if c.Tracks != nil {
		for _, cl := range slices.Concat(c.Tracks.Video, c.Tracks.Audio) {
			ids = append(ids, cl.Media)
		}
		return ids
	}
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

// tracks returns the multi-track form; the older shot list is laid out end to end, with lines as dialogue and the BGM looped.
func (c *ComposeSpec) tracks(files map[string]File) (Tracks, error) {
	if c.Tracks != nil {
		return *c.Tracks, nil
	}
	var t Tracks
	native, dialogue, bgm := vol(c.Volumes.Native, 1), vol(c.Volumes.Dialogue, 1), vol(c.Volumes.BGM, 0.3)
	at := 0.0
	for i, cl := range c.Clips {
		f, ok := files[cl.Media]
		if !ok || f.Kind != "video" {
			return t, failure("CLIP_MISSING", fmt.Sprintf("第 %d 个镜头的视频找不到了，请重新采用", i+1))
		}
		full := float64(f.DurationMS) / 1000
		out := cl.Out
		if out == 0 || out > full {
			out = full
		}
		if cl.In >= out {
			return t, failure("CLIP_RANGE", fmt.Sprintf("第 %d 个镜头的裁剪范围超出了视频长度（%.1f 秒）", i+1, full))
		}
		d := out - cl.In + cl.Hold
		v := TrackClip{Media: cl.Media, Start: at, In: cl.In, Out: out, Hold: cl.Hold, Volume: &native}
		if c.Transition == "fade" && d > 1 {
			v.FadeIn, v.FadeOut = 0.3, 0.3
		}
		t.Video = append(t.Video, v)
		at += d
	}
	for _, l := range c.Lines {
		t.Audio = append(t.Audio, TrackClip{Media: l.Media, Role: "dialogue", Start: l.At, Volume: &dialogue})
		if text := strings.TrimSpace(l.Text); text != "" {
			d := float64(files[l.Media].DurationMS) / 1000
			if d <= 0 {
				d = 2
			}
			t.Subtitles = append(t.Subtitles, Subtitle{Start: l.At, End: l.At + d, Text: text})
		}
	}
	if c.BGM != "" {
		t.Audio = append(t.Audio, TrackClip{Media: c.BGM, Role: "music", Loop: true, FadeOut: 1.5, Volume: &bgm})
	}
	return t, nil
}

// localComposer renders the timeline with ffmpeg into an H.264/AAC MP4 plus an SRT file.
type localComposer struct {
	font string // CJK font file for burned subtitles; empty means burn falls back to soft
}

func (localComposer) Check(context.Context, Job) (Outcome, error) { return Outcome{}, errNotAsync }

func (lc localComposer) Run(ctx context.Context, job Job) (Outcome, error) {
	spec := job.Input.Compose
	t, err := spec.tracks(job.Files)
	if err != nil {
		return Outcome{}, err
	}
	w, h := videoSize(spec.Ratio, spec.Resolution)

	// 视频轨：按开始时间排，片段之间的空隙补黑场和静音，再接成一条。重叠的片段不渲染，直接报错。
	video := slices.Clone(t.Video)
	slices.SortStableFunc(video, func(a, b TrackClip) int { return cmp.Compare(a.Start, b.Start) })
	var args, filters, segs []string
	inputs := 0
	addInput := func(path string, loop bool) int {
		if loop {
			args = append(args, "-stream_loop", "-1")
		}
		args = append(args, "-i", path)
		inputs++
		return inputs - 1
	}
	total := 0.0
	gap := func(d float64) {
		k := len(segs)
		filters = append(filters, fmt.Sprintf("color=c=black:s=%dx%d:r=%d:d=%s,format=yuv420p,setsar=1[g%d]", w, h, spec.FPS, secs(d), k),
			fmt.Sprintf("anullsrc=r=44100:cl=stereo,atrim=end=%s[ga%d]", secs(d), k))
		segs = append(segs, fmt.Sprintf("[g%d][ga%d]", k, k))
	}
	for i, cl := range video {
		f, ok := job.Files[cl.Media]
		if !ok || f.Kind != "video" {
			return Outcome{}, failure("CLIP_MISSING", fmt.Sprintf("第 %d 个视频片段的素材找不到了，请重新采用", i+1))
		}
		full := float64(f.DurationMS) / 1000
		out := cl.Out
		if out == 0 || out > full {
			out = full
		}
		if cl.In >= out {
			return Outcome{}, failure("CLIP_RANGE", fmt.Sprintf("第 %d 个视频片段的入点超出了素材长度（%.1f 秒）", i+1, full))
		}
		switch {
		case cl.Start < total-0.02:
			return Outcome{}, failure("CLIP_OVERLAP", fmt.Sprintf("第 %d 个视频片段和前一段重叠了 %.2f 秒，请先调整位置", i+1, total-cl.Start))
		case cl.Start > total+0.02:
			gap(cl.Start - total)
		}
		d := out - cl.In + cl.Hold
		total = max(total, cl.Start) + d
		in := addInput(f.Path, false)
		// 留边：等比缩小放进画面、补黑边；裁剪：等比放大铺满、裁掉中间以外的部分。都不拉伸。
		fit := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", w, h, w, h)
		if cl.Fit == "crop" {
			fit = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d", w, h, w, h)
		}
		v := fmt.Sprintf("[%d:v]trim=start=%s:end=%s,setpts=PTS-STARTPTS,%s,setsar=1,fps=%d,format=yuv420p",
			in, secs(cl.In), secs(out), fit, spec.FPS)
		if cl.Hold > 0 {
			v += ",tpad=stop_mode=clone:stop_duration=" + secs(cl.Hold)
		}
		if cl.FadeIn > 0 {
			v += ",fade=t=in:st=0:d=" + secs(min(cl.FadeIn, d))
		}
		if cl.FadeOut > 0 {
			v += fmt.Sprintf(",fade=t=out:st=%s:d=%s", secs(max(d-cl.FadeOut, 0)), secs(min(cl.FadeOut, d)))
		}
		k := len(segs)
		filters = append(filters, v+fmt.Sprintf("[v%d]", k))
		if native := vol(cl.Volume, 1); f.HasAudio && native > 0 {
			filters = append(filters, fmt.Sprintf("[%d:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aresample=44100,aformat=channel_layouts=stereo,apad=whole_dur=%s,atrim=end=%s,volume=%s%s[a%d]",
				in, secs(cl.In), secs(out), secs(d), secs(d), secs(native), fades(cl, d), k))
		} else {
			filters = append(filters, fmt.Sprintf("anullsrc=r=44100:cl=stereo,atrim=end=%s[a%d]", secs(d), k))
		}
		segs = append(segs, fmt.Sprintf("[v%d][a%d]", k, k))
	}
	filters = append(filters, strings.Join(segs, "")+fmt.Sprintf("concat=n=%d:v=1:a=1[vcat][acat]", len(segs)))

	// 声音轨：每段裁好、调音量和淡入淡出，挪到开始时间，按对白（含旁白）、配乐、其他三组混；对白期间配乐按 Duck 压低。
	buses := map[string][]string{}
	var voice [][2]float64
	for i, cl := range t.Audio {
		f, ok := job.Files[cl.Media]
		if !ok || f.Kind != "audio" {
			return Outcome{}, failure("AUDIO_MISSING", fmt.Sprintf("第 %d 段声音的素材找不到了，请重新采用或上传", i+1))
		}
		full := float64(f.DurationMS) / 1000
		out := cl.Out
		if out == 0 || out > full {
			out = full
		}
		if cl.Start >= total {
			continue // 在成片结尾之后，听不到
		}
		d := min(out-cl.In, total-cl.Start)
		if cl.Loop {
			d = total - cl.Start
		}
		if d <= 0 {
			return Outcome{}, failure("AUDIO_RANGE", fmt.Sprintf("第 %d 段声音的入点超出了素材长度（%.1f 秒）", i+1, full))
		}
		in := addInput(f.Path, cl.Loop)
		ms := int(cl.Start * 1000)
		chain := fmt.Sprintf("[%d:a]aresample=44100,aformat=channel_layouts=stereo,atrim=start=%s", in, secs(cl.In))
		if !cl.Loop {
			chain += ":end=" + secs(out)
		}
		chain += fmt.Sprintf(",asetpts=PTS-STARTPTS,atrim=end=%s,volume=%s%s,adelay=%d|%d[s%d]", secs(d), secs(vol(cl.Volume, 1)), fades(cl, d), ms, ms, i)
		filters = append(filters, chain)
		bus := map[string]string{"dialogue": "voice", "narration": "voice", "music": "music"}[cl.Role]
		if bus == "" {
			bus = "other"
		}
		buses[bus] = append(buses[bus], fmt.Sprintf("[s%d]", i))
		if bus == "voice" {
			voice = append(voice, [2]float64{cl.Start, cl.Start + d})
		}
	}
	mix := []string{"[acat]"}
	for _, bus := range []string{"voice", "music", "other"} {
		ins := buses[bus]
		if len(ins) == 0 {
			continue
		}
		label := "[" + bus + "]"
		if len(ins) == 1 {
			filters = append(filters, ins[0]+"anull"+label)
		} else {
			filters = append(filters, strings.Join(ins, "")+fmt.Sprintf("amix=inputs=%d:normalize=0", len(ins))+label)
		}
		if bus == "music" && t.Duck < 0 && len(voice) > 0 {
			var on []string
			for _, r := range voice {
				on = append(on, fmt.Sprintf("between(t,%s,%s)", secs(r[0]), secs(r[1])))
			}
			filters = append(filters, fmt.Sprintf("[music]volume=volume=%s:enable='%s'[ducked]", secs(math.Pow(10, t.Duck/20)), strings.Join(on, "+")))
			label = "[ducked]"
		}
		mix = append(mix, label)
	}
	level := ""
	if spec.Loudness != 0 {
		level = fmt.Sprintf("loudnorm=I=%s:TP=-1.5:LRA=11,aresample=44100,", secs(spec.Loudness))
	}
	filters = append(filters, strings.Join(mix, "")+fmt.Sprintf("amix=inputs=%d:duration=first:normalize=0,%salimiter=limit=0.95[aout]", len(mix), level))
	audioOut := "[aout]"
	input := inputs // the SRT, when muxed as a soft track, is the next input

	// 字幕
	srt := buildSRT(t.Subtitles)
	srtPath := filepath.Join(job.Work, "film.srt")
	if err := os.WriteFile(srtPath, []byte(srt), 0o600); err != nil {
		return Outcome{}, err
	}
	plain := slices.Clone(filters)
	videoOut := "[vcat]"
	burned := false
	if spec.Subtitles == "burn" && srt != "" && lc.font != "" {
		// libass 按 288 高的画布换算样式，所以字号和边距写成这个尺度下的值；竖屏字小一点。
		size := 14
		if h > w {
			size = 10
		}
		style := fmt.Sprintf("FontName=Noto Sans CJK SC,FontSize=%d,PrimaryColour=&H00FFFFFF,OutlineColour=&H90000000,BorderStyle=1,Outline=1,Shadow=0,MarginV=%d", size, 22)
		filters = append(filters, fmt.Sprintf("[vcat]subtitles=filename='%s':fontsdir='%s':force_style='%s'[vsub]",
			escapeFilter(srtPath), escapeFilter(filepath.Dir(lc.font)), style))
		videoOut = "[vsub]"
		burned = true
	}
	out := filepath.Join(job.Work, "film.mp4")
	soft := spec.Subtitles != "none" && srt != "" && !burned
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	encode := func(path, video string, graph []string, soft bool) error {
		cmd := append([]string{"-v", "error", "-y"}, args...)
		if soft {
			cmd = append(cmd, "-i", srtPath)
		}
		cmd = append(cmd, "-filter_complex", strings.Join(graph, ";"), "-map", video, "-map", audioOut)
		if soft {
			cmd = append(cmd, "-map", fmt.Sprintf("%d:s", input), "-c:s", "mov_text", "-metadata:s:s:0", "language=chi")
		}
		cmd = append(cmd, "-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p", "-r", strconv.Itoa(spec.FPS),
			"-c:a", "aac", "-b:a", "160k", "-ar", "44100", "-movflags", "+faststart", "-t", secs(total), path)
		if msg, err := exec.CommandContext(ctx, "ffmpeg", cmd...).CombinedOutput(); err != nil {
			return &Failure{Code: "COMPOSE_FAILED", Message: "合成失败：" + short(lastLine(string(msg))) + "。请检查片段后重试"}
		}
		return nil
	}
	if err := encode(out, videoOut, filters, soft); err != nil {
		return Outcome{}, err
	}
	music := slices.ContainsFunc(t.Audio, func(c TrackClip) bool { return c.Role == "music" })
	manifest := map[string]any{"width": w, "height": h, "ratio": spec.Ratio, "resolution": spec.Resolution, "fps": spec.FPS,
		"duration_ms": int(total * 1000), "clips": len(t.Video), "lines": len(buses["voice"]), "audio_clips": len(t.Audio),
		"subtitles": subtitleMode(spec.Subtitles, burned, srt != ""), "subtitle_count": strings.Count(srt, " --> "),
		"bgm": music, "transition": spec.Transition, "duck_db": t.Duck}
	if spec.Loudness != 0 {
		manifest["loudness_target"] = spec.Loudness
	}
	// 整体响度和真峰值：只测量，写进成片说明；按交付预设调整的在上面混音时已经做了。
	if lufs, peak, ok := measureLoudness(ctx, out); ok {
		manifest["loudness_lufs"], manifest["true_peak_db"] = lufs, peak
	}
	if spec.Subtitles == "burn" && !burned {
		manifest["subtitles_note"] = "服务器没有中文字体，字幕改为内嵌字幕轨（播放器里可开关）"
	}
	files := []Produced{{Path: out, Ext: "mp4", Kind: "video", Meta: map[string]any{"manifest": manifest}}}
	if srt != "" {
		files = append(files, Produced{Path: srtPath, Ext: "srt", Kind: "subtitle"})
	}
	if spec.Clean && (burned || soft) {
		clean := filepath.Join(job.Work, "film-clean.mp4")
		if err := encode(clean, "[vcat]", plain, false); err != nil {
			return Outcome{}, err
		}
		cm := maps.Clone(manifest)
		cm["subtitles"] = "none"
		delete(cm, "subtitles_note")
		files = append(files, Produced{Path: clean, Ext: "mp4", Kind: "video", Meta: map[string]any{"clean": true, "manifest": cm}})
	}
	return Outcome{Files: files, Result: map[string]any{"manifest": manifest}}, nil
}

// measureLoudness runs ffmpeg loudnorm in analysis mode: integrated loudness (LUFS) and true peak (dBTP).
func measureLoudness(ctx context.Context, path string) (lufs, peak float64, ok bool) {
	raw, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-nostats", "-i", path, "-vn", "-af", "loudnorm=print_format=json", "-f", "null", "-").CombinedOutput()
	if err != nil {
		return 0, 0, false
	}
	text := string(raw)
	i := strings.LastIndex(text, "{")
	j := strings.LastIndex(text, "}")
	if i < 0 || j < i {
		return 0, 0, false
	}
	var m struct {
		I  string `json:"input_i"`
		TP string `json:"input_tp"`
	}
	if json.Unmarshal([]byte(text[i:j+1]), &m) != nil {
		return 0, 0, false
	}
	lufs, err1 := strconv.ParseFloat(m.I, 64)
	peak, err2 := strconv.ParseFloat(m.TP, 64)
	if err1 != nil || err2 != nil || math.IsInf(lufs, 0) {
		return 0, 0, false // 整段静音时 loudnorm 给 -inf
	}
	return math.Round(lufs*10) / 10, math.Round(peak*10) / 10, true
}

func buildSRT(subs []Subtitle) string {
	var b strings.Builder
	n := 0
	stamp := func(s float64) string {
		ms := int(s * 1000)
		return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
	}
	subs = slices.Clone(subs)
	slices.SortStableFunc(subs, func(a, b Subtitle) int { return cmp.Compare(a.Start, b.Start) })
	for _, s := range subs {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", n, stamp(s.Start), stamp(s.End), text)
	}
	return b.String()
}

// fades is the afade part of an audio chain for a clip lasting d seconds.
func fades(c TrackClip, d float64) string {
	var f string
	if c.FadeIn > 0 {
		f += ",afade=t=in:st=0:d=" + secs(min(c.FadeIn, d))
	}
	if c.FadeOut > 0 {
		f += fmt.Sprintf(",afade=t=out:st=%s:d=%s", secs(max(d-c.FadeOut, 0)), secs(min(c.FadeOut, d)))
	}
	return f
}

func escapeFilter(p string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`, `:`, `\:`).Replace(p)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// subtitleMode is what the film actually got: burn falls back to a soft track without a font.
func subtitleMode(asked string, burned, any bool) string {
	switch {
	case !any || asked == "none":
		return "none"
	case burned:
		return "burn"
	}
	return "soft"
}
