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
