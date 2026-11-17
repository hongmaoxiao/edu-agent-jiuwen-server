package comic

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// store keeps media files under one directory: <account>/<project>/<media id>.<ext>.
type store struct {
	dir string
	key []byte // signs media URLs, so <img>/<video> tags can load files without the bearer token
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("comic media directory: %w", err)
	}
	keyFile := filepath.Join(dir, ".url-key")
	key, err := os.ReadFile(keyFile)
	if err != nil || len(key) < 32 {
		key = make([]byte, 32)
		rand.Read(key)
		if err := os.WriteFile(keyFile, key, 0o600); err != nil {
			return nil, fmt.Errorf("comic media url key: %w", err)
		}
	}
	return &store{dir: dir, key: key}, nil
}

func (s *store) abs(rel string) string { return filepath.Join(s.dir, filepath.FromSlash(rel)) }

// sign returns the query string that lets a media URL be fetched until exp.
func (s *store) sign(id string, exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(id + "|" + e))
	return "e=" + e + "&s=" + hex.EncodeToString(mac.Sum(nil))
}

func (s *store) verify(id, e, sig string, now time.Time) bool {
	unix, err := strconv.ParseInt(e, 10, 64)
	if err != nil || now.Unix() > unix {
		return false
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(id + "|" + e))
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sig))
}

// probed is what ffprobe says about a file.
type probed struct {
	Width, Height, DurationMS int
	HasAudio, HasVideo        bool
}

func probe(ctx context.Context, path string) (probed, error) {
	var p probed
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-print_format", "json",
		"-show_entries", "stream=codec_type,width,height,duration:format=duration", path).Output()
	if err != nil {
		return p, fmt.Errorf("%w: 文件无法识别，可能已损坏或格式不支持", ErrRejected)
	}
	var info struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Duration  string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return p, err
	}
	seconds, _ := strconv.ParseFloat(info.Format.Duration, 64)
	for _, st := range info.Streams {
		switch st.CodecType {
		case "video":
			p.HasVideo = true
			if p.Width == 0 {
				p.Width, p.Height = st.Width, st.Height
			}
			if seconds == 0 {
				seconds, _ = strconv.ParseFloat(st.Duration, 64)
			}
		case "audio":
			p.HasAudio = true
			if seconds == 0 {
				seconds, _ = strconv.ParseFloat(st.Duration, 64)
			}
		}
	}
	p.DurationMS = int(seconds * 1000)
	return p, nil
}

var mimeOf = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "webp": "image/webp",
	"mp3": "audio/mpeg", "wav": "audio/wav", "m4a": "audio/mp4",
	"mp4": "video/mp4", "mov": "video/quicktime", "srt": "application/x-subrip", "json": "application/json",
}

// still images report a 0.04 s "duration" from ffprobe; anything image-like is treated as a still.
func kindFromProbe(p probed, ext string) string {
	switch {
	case strings.HasPrefix(mimeOf[ext], "image/"):
		return "image"
	case p.HasVideo:
		return "video"
	case p.HasAudio:
		return "audio"
	}
	return ""
}

// shrinkImage returns a JPEG no larger than 2048 px on the long side, for sending to a provider.
func shrinkImage(ctx context.Context, f File, work string) (File, error) {
	if f.Width > 0 && f.Width <= 2048 && f.Height <= 2048 && (f.Mime == "image/jpeg" || f.Mime == "image/png") {
		return f, nil
	}
	out := filepath.Join(work, "ref-"+f.ID+".jpg")
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-y", "-i", f.Path,
		"-vf", "scale='min(2048,iw)':'min(2048,ih)':force_original_aspect_ratio=decrease", "-q:v", "3", out)
	if err := cmd.Run(); err != nil {
		return f, failure("REF_UNREADABLE", "参考图无法处理，请换一张")
	}
	return File{ID: f.ID, Path: out, Kind: "image", Mime: "image/jpeg"}, nil
}
