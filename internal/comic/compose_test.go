package comic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 烧录字幕时勾了清洁版：另出一份同长度、没有字幕的 MP4；字幕只在烧录版里。
func TestComposeBurnedFilmWithCleanCopy(t *testing.T) {
	font := "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc"
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := os.Stat(font); err != nil {
		t.Skip("CJK font not installed")
	}
	work := t.TempDir()
	clip, line := filepath.Join(work, "clip.mp4"), filepath.Join(work, "line.mp3")
	for _, args := range [][]string{
		{"-f", "lavfi", "-i", "color=c=gray:s=240x426:d=2", "-f", "lavfi", "-i", "sine=f=440:d=2", "-shortest", "-pix_fmt", "yuv420p", clip},
		{"-f", "lavfi", "-i", "sine=f=660:d=1", line},
	} {
		if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	spec := &ComposeSpec{Ratio: "9:16", Resolution: "480p", Subtitles: "burn", Clean: true,
		Clips: []Clip{{Media: "v"}}, Lines: []TLLine{{Media: "a", At: 0.3, Text: "这一次，我想试试。"}}}
	if err := spec.check(); err != nil {
		t.Fatal(err)
	}
	job := Job{Input: Input{Compose: spec}, Work: work, Files: map[string]File{
		"v": {Path: clip, Kind: "video", DurationMS: 2000, HasAudio: true},
		"a": {Path: line, Kind: "audio", DurationMS: 1000},
	}}
	out, err := localComposer{font: font}.Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 3 || out.Files[2].Meta["clean"] != true || out.Files[2].Meta["manifest"].(map[string]any)["subtitles"] != "none" {
		t.Fatalf("files: %+v", out.Files)
	}
	probe := func(path string) (subs int, seconds float64) {
		raw, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type:format=duration", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Fields(string(raw)) {
			if l == "subtitle" {
				subs++
			} else if v, err := strconv.ParseFloat(l, 64); err == nil {
				seconds = v
			}
		}
		return
	}
	burnSubs, burnLen := probe(out.Files[0].Path)
	cleanSubs, cleanLen := probe(out.Files[2].Path)
	if burnSubs != 0 || cleanSubs != 0 || burnLen < 1.9 || cleanLen-burnLen > 0.05 || burnLen-cleanLen > 0.05 {
		t.Fatalf("burned %d subs %.2fs, clean %d subs %.2fs", burnSubs, burnLen, cleanSubs, cleanLen)
	}
	// 两份画面不一样：烧录版底部有字。
	frame := func(path string) []byte {
		b, err := exec.Command("ffmpeg", "-v", "error", "-ss", "1", "-i", path, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if string(frame(out.Files[0].Path)) == string(frame(out.Files[2].Path)) {
		t.Fatal("burned and clean films have the same picture")
	}
}

// 多轨：空隙补黑场、定格、配乐循环到结尾；对白期间配乐压低；成片说明里有响度。
func TestComposeTracksGapDuckAndLoudness(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	work := t.TempDir()
	clip, line, music := filepath.Join(work, "clip.mp4"), filepath.Join(work, "line.mp3"), filepath.Join(work, "music.mp3")
	for _, args := range [][]string{
		{"-f", "lavfi", "-i", "color=c=gray:s=240x426:d=2", "-f", "lavfi", "-i", "sine=f=440:d=2", "-shortest", "-pix_fmt", "yuv420p", clip},
		{"-f", "lavfi", "-i", "sine=f=660:d=1", line},
		{"-f", "lavfi", "-i", "sine=f=220:d=3", music},
	} {
		if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v %s", err, out)
		}
	}
	zero, half := 0.0, 0.5
	files := map[string]File{
		"v": {Path: clip, Kind: "video", DurationMS: 2000, HasAudio: true},
		"a": {Path: line, Kind: "audio", DurationMS: 1000},
		"m": {Path: music, Kind: "audio", DurationMS: 3000},
	}
	// 原生声和对白都静音，只剩配乐：对白所在的 1.0–2.0 秒应该比后面安静约 18 dB。
	spec := &ComposeSpec{Ratio: "9:16", Resolution: "480p", Subtitles: "none", Tracks: &Tracks{
		Video: []TrackClip{{Media: "v", Start: 0, Out: 2, Volume: &zero}, {Media: "v", Start: 2.5, In: 0.5, Hold: 0.5, Volume: &zero}},
		Audio: []TrackClip{{Media: "a", Role: "dialogue", Start: 1, Volume: &zero}, {Media: "m", Role: "music", Loop: true, Volume: &half}},
		Duck:  -18,
	}}
	if err := spec.check(); err != nil {
		t.Fatal(err)
	}
	out, err := localComposer{}.Run(context.Background(), Job{Input: Input{Compose: spec}, Work: work, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Files[0].Meta["manifest"].(map[string]any)
	if m["duration_ms"] != 4500 || m["clips"] != 2 || m["loudness_lufs"] == nil || m["true_peak_db"] == nil {
		t.Fatalf("manifest: %v", m)
	}
	raw, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", out.Files[0].Path).Output()
	if d, _ := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64); d < 4.4 || d > 4.6 {
		t.Fatalf("film length %.2f, want 4.5 (2 + 0.5 gap + 1.5 + 0.5 hold)", d)
	}
	level := func(from float64) float64 {
		b, _ := exec.Command("ffmpeg", "-v", "info", "-ss", strconv.FormatFloat(from, 'f', 1, 64), "-t", "0.6", "-i", out.Files[0].Path, "-af", "volumedetect", "-f", "null", "-").CombinedOutput()
		s := string(b)
		i := strings.Index(s, "mean_volume: ")
		if i < 0 {
			t.Fatalf("volumedetect: %s", s)
		}
		v, _ := strconv.ParseFloat(strings.Fields(s[i+len("mean_volume: "):])[0], 64)
		return v
	}
	if ducked, open := level(1.2), level(3.2); open-ducked < 12 {
		t.Fatalf("music under dialogue %.1f dB, elsewhere %.1f dB: not ducked", ducked, open)
	}
	// 视频片段重叠：直接报错，不偷偷挪位置。
	spec.Tracks.Video[1].Start = 1.5
	if _, err := (localComposer{}).Run(context.Background(), Job{Input: Input{Compose: spec}, Work: work, Files: files}); err == nil || !strings.Contains(err.Error(), "重叠") {
		t.Fatalf("overlap: %v", err)
	}
}

func TestTracksCheck(t *testing.T) {
	ok := func() *ComposeSpec {
		return &ComposeSpec{Ratio: "9:16", Resolution: "480p", Tracks: &Tracks{Video: []TrackClip{{Media: "v"}}}}
	}
	for name, bad := range map[string]func(*ComposeSpec){
		"no video":    func(c *ComposeSpec) { c.Tracks.Video = nil },
		"negative":    func(c *ComposeSpec) { c.Tracks.Video[0].Start = -1 },
		"short range": func(c *ComposeSpec) { c.Tracks.Video[0].In, c.Tracks.Video[0].Out = 1, 1.1 },
		"role":        func(c *ComposeSpec) { c.Tracks.Audio = []TrackClip{{Media: "a", Role: "native"}} },
		"duck up":     func(c *ComposeSpec) { c.Tracks.Duck = 3 },
		"subtitle":    func(c *ComposeSpec) { c.Tracks.Subtitles = []Subtitle{{Start: 2, End: 1, Text: "x"}} },
		"loudness":    func(c *ComposeSpec) { c.Loudness = -5 },
		"video loop":  func(c *ComposeSpec) { c.Tracks.Video[0].Loop = true },
		"voice loop":  func(c *ComposeSpec) { c.Tracks.Audio = []TrackClip{{Media: "a", Role: "dialogue", Loop: true}} },
	} {
		c := ok()
		bad(c)
		if c.check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ok().check(); err != nil {
		t.Fatal(err)
	}
	// 音乐和环境声可以循环。
	for _, role := range []string{"music", "ambience"} {
		c := ok()
		c.Tracks.Audio = []TrackClip{{Media: "a", Role: role, Loop: true}}
		if err := c.check(); err != nil {
			t.Errorf("%s loop rejected: %v", role, err)
		}
	}
}
