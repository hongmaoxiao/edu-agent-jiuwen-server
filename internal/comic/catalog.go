package comic

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Task kinds. Each kind runs on models of one model kind (see kindOf).
const (
	KindParse      = "parse"      // 剧本解析：角色、场景、道具、镜头
	KindStoryboard = "storyboard" // 按已确认的资产重写分镜与提示词
	KindImage      = "image"      // 资产主图 / 三视图 / 镜头参考画面
	KindVoice      = "voice"      // 文字描述定制音色
	KindSpeech     = "speech"     // 一句台词的音频
	KindVideo      = "video"      // 一个镜头的视频
	KindCompose    = "compose"    // 合成成片（本机 ffmpeg，不调模型）
)

var kindOf = map[string]string{
	KindParse: "text", KindStoryboard: "text", KindImage: "image", KindVoice: "voice",
	KindSpeech: "speech", KindVideo: "video", KindCompose: "compose",
}

// Model is one entry of the catalog the client shows. Everything a control needs (allowed values,
// price, expected wait) comes from here, so a model can be added or repriced without a client release.
type Model struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"` // text | image | voice | speech | video | compose
	Provider    string   `json:"provider"`
	Name        string   `json:"name"`
	Purpose     string   `json:"purpose"`
	ETASeconds  int      `json:"eta_seconds"`
	Simulated   bool     `json:"simulated"`
	Available   bool     `json:"available"`
	Unavailable string   `json:"unavailable,omitempty"`
	Durations   []int    `json:"durations,omitempty"`   // 视频可选秒数
	Resolutions []string `json:"resolutions,omitempty"` // 视频分辨率 / 图片面积档
	Ratios      []string `json:"ratios,omitempty"`
	NativeAudio bool     `json:"native_audio,omitempty"` // 视频自带声音
	AudioToggle bool     `json:"audio_toggle,omitempty"` // 可以关掉自带声音
	FirstFrame  bool     `json:"first_frame,omitempty"`  // 支持首帧图
	MaxRefs     int      `json:"max_refs,omitempty"`     // 参考图上限
	MaxCount    int      `json:"max_count,omitempty"`    // 一次出几张
	Formats     []string `json:"formats,omitempty"`
	Emotions    []string `json:"emotions,omitempty"`
	Price       Price    `json:"price"`
	remote      string
}

// Price is in 创作点. Video is per second per resolution, images per picture per size,
// speech per started 100 characters, the rest a flat amount per task.
type Price struct {
	PerSecond   map[string]int `json:"per_second,omitempty"`
	PerImage    map[string]int `json:"per_image,omitempty"`
	Per100Chars int            `json:"per_100_chars,omitempty"`
	Flat        int            `json:"flat,omitempty"`
}

var ratios = []string{"9:16", "16:9", "1:1", "3:4", "4:3", "21:9"}
var speechEmotions = []string{"happy", "sad", "angry", "fearful", "disgusted", "surprised", "calm"}

// defaultCatalog lists what the platform can run. Prices follow the providers' public price lists
// (2026-11) at 1 创作点 ≈ 0.1 元 and are rounded up; the operator can change them here without touching the client.
func defaultCatalog() []Model {
	seconds := func(from, to int) []int {
		var out []int
		for d := from; d <= to; d++ {
			out = append(out, d)
		}
		return out
	}
	return []Model{
		{ID: "deepseek-chat", Kind: "text", Provider: "text", Name: "DeepSeek", Purpose: "解析剧本、拆分镜头、写画面与视频提示词", ETASeconds: 40,
			Price: Price{Flat: 2}, remote: "deepseek-chat"},

		{ID: "minimax-h3-image", Kind: "image", Provider: "minimax", Name: "MiniMax H3 Image", Purpose: "角色立绘、场景、道具；可带参考图保持一致", ETASeconds: 60,
			Resolutions: []string{"1K", "2K", "4K"}, Ratios: ratios, MaxRefs: 8, MaxCount: 4, Formats: []string{"png", "jpeg", "webp"},
			Price: Price{PerImage: map[string]int{"1K": 1, "2K": 2, "4K": 4}}, remote: "MiniMax-H3-Image"},
		{ID: "seedream-5.5", Kind: "image", Provider: "ark", Name: "Seedream 5.5", Purpose: "插画与场景质感好；可带参考图", ETASeconds: 45,
			Resolutions: []string{"1K", "2K"}, Ratios: ratios, MaxRefs: 8, MaxCount: 4, Formats: []string{"png", "jpeg"},
			Price: Price{PerImage: map[string]int{"1K": 3, "2K": 3}}, remote: "doubao-seedream-5-5-261028"},

		{ID: "minimax-voice-design", Kind: "voice", Provider: "minimax", Name: "MiniMax 音色设计", Purpose: "按文字描述生成新音色（首次用它配音时模型方收 9.9 元）", ETASeconds: 20,
			Price: Price{Flat: 99}, remote: "voice_design"},
		{ID: "speech-2.8-hd", Kind: "speech", Provider: "minimax", Name: "MiniMax Speech 2.8 HD", Purpose: "台词配音，音质高", ETASeconds: 8,
			Emotions: speechEmotions, Price: Price{Per100Chars: 1}, remote: "speech-2.8-hd"},
		{ID: "speech-2.8-turbo", Kind: "speech", Provider: "minimax", Name: "MiniMax Speech 2.8 Turbo", Purpose: "台词配音，更快", ETASeconds: 5,
			Emotions: speechEmotions, Price: Price{Per100Chars: 1}, remote: "speech-2.8-turbo"},

		{ID: "minimax-h3", Kind: "video", Provider: "minimax", Name: "MiniMax H3", Purpose: "画质优先，原生带声音（对白、环境声）", ETASeconds: 420,
			Durations: seconds(4, 15), Resolutions: []string{"768P", "2K"}, Ratios: ratios, NativeAudio: true, FirstFrame: true, MaxRefs: 9,
			Price: Price{PerSecond: map[string]int{"768P": 5, "2K": 8}}, remote: "MiniMax-H3"},
		{ID: "minimax-h3-highspeed", Kind: "video", Provider: "minimax", Name: "MiniMax H3 Highspeed", Purpose: "质量与速度兼顾，原生带声音", ETASeconds: 240,
			Durations: seconds(4, 15), Resolutions: []string{"480P", "768P"}, Ratios: ratios, NativeAudio: true, FirstFrame: true, MaxRefs: 9,
			Price: Price{PerSecond: map[string]int{"480P": 3, "768P": 4}}, remote: "MiniMax-H3-Highspeed"},
		{ID: "seedance-2.0", Kind: "video", Provider: "ark", Name: "Seedance 2.0", Purpose: "画面稳定、运镜丰富；可关掉自带声音", ETASeconds: 300,
			Durations: seconds(4, 15), Resolutions: []string{"480p", "720p", "1080p"}, Ratios: ratios, NativeAudio: true, AudioToggle: true, FirstFrame: true, MaxRefs: 9,
			Price: Price{PerSecond: map[string]int{"480p": 4, "720p": 6, "1080p": 10}}, remote: "doubao-seedance-2-0-260128"},
		{ID: "seedance-2.0-fast", Kind: "video", Provider: "ark", Name: "Seedance 2.0 Fast", Purpose: "出片更快更便宜，最高 720p", ETASeconds: 150,
			Durations: seconds(4, 15), Resolutions: []string{"480p", "720p"}, Ratios: ratios, NativeAudio: true, AudioToggle: true, FirstFrame: true, MaxRefs: 9,
			Price: Price{PerSecond: map[string]int{"480p": 3, "720p": 4}}, remote: "doubao-seedance-2-0-fast-260128"},

		{ID: "compose", Kind: "compose", Provider: "local", Name: "平台合成", Purpose: "按时间轴拼接镜头、混合对白与配乐、加字幕，导出 MP4", ETASeconds: 60},

		// 模拟模型：只在 EDU_COMIC_SIMULATE=1 时出现。生成真实的小文件，不扣点，界面上必须标「模拟」。
		{ID: "sim-text", Kind: "text", Provider: "sim", Simulated: true, Name: "模拟解析", Purpose: "按「人物：/场景：/镜头N」格式拆剧本，不调模型", ETASeconds: 3},
		{ID: "sim-image", Kind: "image", Provider: "sim", Simulated: true, Name: "模拟生图", Purpose: "生成色块测试图，不调模型", ETASeconds: 4,
			Resolutions: []string{"1K", "2K"}, Ratios: ratios, MaxRefs: 8, MaxCount: 4, Formats: []string{"png"}},
		{ID: "sim-voice", Kind: "voice", Provider: "sim", Simulated: true, Name: "模拟音色", Purpose: "生成测试音色，不调模型", ETASeconds: 3},
		{ID: "sim-speech", Kind: "speech", Provider: "sim", Simulated: true, Name: "模拟配音", Purpose: "按字数生成提示音，不调模型", ETASeconds: 3, Emotions: speechEmotions},
		{ID: "sim-video", Kind: "video", Provider: "sim", Simulated: true, Name: "模拟视频", Purpose: "用首帧图做推镜测试片，不调模型", ETASeconds: 8,
			Durations: seconds(2, 15), Resolutions: []string{"480p", "720p"}, Ratios: ratios, NativeAudio: true, AudioToggle: true, FirstFrame: true, MaxRefs: 9},
		// 能力受限的模拟视频：用来在本机验证「切模型差异」（时长只有 5/10 秒、只出 480p、不收首帧、最多 2 张参考、不出声）。
		{ID: "sim-video-lite", Kind: "video", Provider: "sim", Simulated: true, Name: "模拟视频（受限）", Purpose: "能力受限的测试模型，不调模型", ETASeconds: 8,
			Durations: []int{5, 10}, Resolutions: []string{"480p"}, Ratios: ratios, MaxRefs: 2},
	}
}

// Voice is a preset voice offered for binding.
type Voice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Gender string `json:"gender"` // male | female | child
	Age    string `json:"age"`    // 少年 | 青年 | 成年 | 中年 | 老年
	Style  string `json:"style"`
}

// presetVoices are MiniMax system voices (docs: faq/system-voice-id).
var presetVoices = []Voice{
	{"male-qn-daxuesheng", "青年大学生", "male", "青年", "清朗"},
	{"male-qn-qingse", "青涩青年", "male", "青年", "温和"},
	{"chunzhen_xuedi", "纯真学弟", "male", "少年", "纯真"},
	{"lengdan_xiongzhang", "冷淡学长", "male", "青年", "冷静"},
	{"junlang_nanyou", "俊朗男友", "male", "青年", "温柔"},
	{"male-qn-jingying", "精英青年", "male", "青年", "干练"},
	{"Chinese (Mandarin)_Gentleman", "温润男声", "male", "成年", "温润"},
	{"Chinese (Mandarin)_Unrestrained_Young_Man", "不羁青年", "male", "青年", "洒脱"},
	{"Chinese (Mandarin)_Southern_Young_Man", "南方小哥", "male", "青年", "亲切"},
	{"Chinese (Mandarin)_Reliable_Executive", "沉稳高管", "male", "中年", "沉稳"},
	{"Chinese (Mandarin)_Humorous_Elder", "搞笑大爷", "male", "老年", "幽默"},
	{"female-shaonv", "少女", "female", "少年", "清甜"},
	{"danya_xuejie", "淡雅学姐", "female", "青年", "淡雅"},
	{"qiaopi_mengmei", "俏皮萌妹", "female", "少年", "俏皮"},
	{"tianxin_xiaoling", "甜心小玲", "female", "青年", "甜美"},
	{"Chinese (Mandarin)_Warm_Bestie", "温暖闺蜜", "female", "青年", "开朗"},
	{"female-yujie", "御姐", "female", "成年", "成熟"},
	{"Chinese (Mandarin)_Sweet_Lady", "甜美女声", "female", "成年", "甜美"},
	{"Chinese (Mandarin)_Wise_Women", "阅历姐姐", "female", "成年", "知性"},
	{"Chinese (Mandarin)_News_Anchor", "新闻女声", "female", "成年", "播报"},
	{"Chinese (Mandarin)_Kind-hearted_Antie", "热心大婶", "female", "中年", "热心"},
	{"clever_boy", "聪明男童", "child", "儿童", "机灵"},
	{"lovely_girl", "萌萌女童", "child", "儿童", "可爱"},
}

// Style is a look preset applied to every image and video prompt of a project.
type Style struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

var presetStyles = []Style{
	{"healing-campus", "2D 治愈校园", "日系二维动画风格，柔和描边，暖色调，干净的校园光影，治愈氛围"},
	{"cel-anime", "赛璐璐动画", "赛璐璐上色的日式动画，清晰轮廓线，平涂阴影，明快配色"},
	{"guofeng-ink", "国风水墨", "中国水墨与工笔结合的国风插画，留白，淡彩，宣纸质感"},
	{"comic-ink", "美漫线稿", "美式漫画风格，粗黑描边，网点阴影，强烈明暗对比"},
	{"3d-cartoon", "3D 卡通", "三维卡通渲染，圆润造型，柔和全局光照，细腻材质"},
	{"film-real", "电影写实", "电影级写实画面，自然光，浅景深，胶片颗粒"},
	{"pixel", "像素风", "16 位像素艺术风格，有限调色板，清晰像素块"},
	{"watercolor", "水彩绘本", "儿童绘本水彩风格，纸张纹理，晕染边缘，温柔配色"},
}

// Input is everything a task may carry; each kind reads its own fields (validated in check).
type Input struct {
	// parse / storyboard
	Script  string       `json:"script,omitempty"`
	Density string       `json:"density,omitempty"` // 精简 | 标准 | 详细
	Style   string       `json:"style,omitempty"`   // 风格描述（预设或自定义）
	Assets  []AssetBrief `json:"assets,omitempty"`  // storyboard：已确认的资产
	Shots   []ShotBrief  `json:"shots,omitempty"`   // storyboard：现有镜头（保留编号与时长）
	// image / video
	Prompt     string   `json:"prompt,omitempty"`
	Ratio      string   `json:"ratio,omitempty"`
	Resolution string   `json:"resolution,omitempty"` // 图片面积档或视频分辨率
	Count      int      `json:"count,omitempty"`
	Format     string   `json:"format,omitempty"`
	Refs       []string `json:"refs,omitempty"`        // 参考图（媒体 id）
	FirstFrame string   `json:"first_frame,omitempty"` // 首帧图（媒体 id）
	Duration   int      `json:"duration,omitempty"`
	Audio      *bool    `json:"audio,omitempty"` // 视频要不要模型自带声音
	// voice / speech
	Text        string  `json:"text,omitempty"`
	VoiceID     string  `json:"voice_id,omitempty"`
	Speed       float64 `json:"speed,omitempty"`
	Emotion     string  `json:"emotion,omitempty"`
	PreviewText string  `json:"preview_text,omitempty"`
	// compose
	Compose *ComposeSpec `json:"compose,omitempty"`
}

type AssetBrief struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ShotBrief struct {
	ID       string `json:"id"`
	Duration int    `json:"duration"`
	Action   string `json:"action,omitempty"`
	Dialogue string `json:"dialogue,omitempty"`
}

type catalog struct {
	models []Model
	byID   map[string]*Model
}

func newCatalog(models []Model) *catalog {
	c := &catalog{models: models, byID: map[string]*Model{}}
	for i := range c.models {
		c.byID[c.models[i].ID] = &c.models[i]
	}
	return c
}

func rejectf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRejected}, args...)...)
}

// estimate is what a task will be charged, in 创作点. Simulated models are free.
func estimate(m *Model, in Input) int {
	if m.Simulated {
		return 0
	}
	p := m.Price
	switch m.Kind {
	case "video":
		return p.PerSecond[in.Resolution] * in.Duration
	case "image":
		return p.PerImage[in.Resolution] * max(in.Count, 1)
	case "speech":
		return p.Per100Chars * ((utf8.RuneCountInString(in.Text) + 99) / 100)
	}
	return p.Flat
}

// check validates a task's input against its model, with messages a student can act on.
func check(kind string, m *Model, in *Input) error {
	if kindOf[kind] != m.Kind {
		return rejectf("%s 不能用来做这一步", m.Name)
	}
	if !m.Available {
		return rejectf("%s 暂不可用：%s", m.Name, m.Unavailable)
	}
	text := func(value, what string, limit int) error {
		n := utf8.RuneCountInString(strings.TrimSpace(value))
		if n == 0 {
			return rejectf("%s不能为空", what)
		}
		if n > limit {
			return rejectf("%s最多 %d 字，现在 %d 字", what, limit, n)
		}
		return nil
	}
	switch kind {
	case KindParse:
		return text(in.Script, "剧本正文", 5000)
	case KindStoryboard:
		if err := text(in.Script, "剧本正文", 5000); err != nil {
			return err
		}
		if len(in.Assets) > 60 || len(in.Shots) > 60 {
			return rejectf("资产或镜头太多，最多各 60 个")
		}
	case KindImage:
		if err := text(in.Prompt, "提示词", 4000); err != nil {
			return err
		}
		if in.Count == 0 {
			in.Count = 1
		}
		if in.Count < 1 || in.Count > m.MaxCount {
			return rejectf("%s 一次最多生成 %d 张", m.Name, m.MaxCount)
		}
		if in.Ratio == "" {
			in.Ratio = "1:1"
		}
		if !slices.Contains(m.Ratios, in.Ratio) {
			return rejectf("%s 不支持 %s 画幅", m.Name, in.Ratio)
		}
		if in.Resolution == "" {
			in.Resolution = m.Resolutions[0]
		}
		if !slices.Contains(m.Resolutions, in.Resolution) {
			return rejectf("%s 不支持 %s，可选：%s", m.Name, in.Resolution, strings.Join(m.Resolutions, "、"))
		}
		if in.Format == "" {
			in.Format = m.Formats[0]
		}
		if !slices.Contains(m.Formats, in.Format) {
			return rejectf("%s 不能输出 %s 格式", m.Name, in.Format)
		}
		if len(in.Refs) > m.MaxRefs {
			return rejectf("%s 最多带 %d 张参考图", m.Name, m.MaxRefs)
		}
	case KindVoice:
		if err := text(in.Prompt, "音色描述", 300); err != nil {
			return err
		}
		if strings.TrimSpace(in.PreviewText) == "" {
			in.PreviewText = "你好，很高兴认识你。这是我的声音。"
		}
		return text(in.PreviewText, "试听文本", 200)
	case KindSpeech:
		if err := text(in.Text, "台词", 500); err != nil {
			return err
		}
		if strings.TrimSpace(in.VoiceID) == "" {
			return rejectf("先给这个角色绑定音色")
		}
		if in.Speed == 0 {
			in.Speed = 1
		}
		if in.Speed < 0.5 || in.Speed > 2 {
			return rejectf("语速要在 0.5 到 2 之间")
		}
		if in.Emotion != "" && !slices.Contains(m.Emotions, in.Emotion) {
			return rejectf("%s 不支持这种情绪", m.Name)
		}
	case KindVideo:
		if err := text(in.Prompt, "视频提示词", 2000); err != nil {
			return err
		}
		if !slices.Contains(m.Durations, in.Duration) {
			return rejectf("%s 的时长只能是 %d 到 %d 秒", m.Name, m.Durations[0], m.Durations[len(m.Durations)-1])
		}
		if !slices.Contains(m.Resolutions, in.Resolution) {
			return rejectf("%s 不支持 %s，可选：%s", m.Name, in.Resolution, strings.Join(m.Resolutions, "、"))
		}
		if !slices.Contains(m.Ratios, in.Ratio) {
			return rejectf("%s 不支持 %s 画幅", m.Name, in.Ratio)
		}
		if in.FirstFrame != "" && len(in.Refs) > 0 {
			return rejectf("首帧图和参考图只能二选一")
		}
		if len(in.Refs) > m.MaxRefs {
			return rejectf("%s 最多带 %d 张参考图", m.Name, m.MaxRefs)
		}
		if in.Audio != nil && !*in.Audio && !m.AudioToggle {
			return rejectf("%s 生成的视频总是带声音；不需要时在合成里把原声音量调到 0", m.Name)
		}
	case KindCompose:
		if in.Compose == nil {
			return rejectf("缺少合成时间轴")
		}
		return in.Compose.check()
	}
	return nil
}
