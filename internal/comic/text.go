package comic

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Breakdown is what parse and storyboard return. The client shows it for confirmation before
// anything is written into the project, so a bad parse never overwrites the student's work.
type Breakdown struct {
	Characters []Asset `json:"characters"`
	Scenes     []Asset `json:"scenes"`
	Props      []Asset `json:"props"`
	Shots      []Shot  `json:"shots"`
}

// Asset fields use fixed keys (see assetFields) so the client can label them.
type Asset struct {
	Name   string            `json:"name"`
	Fields map[string]string `json:"fields"`
	Prompt string            `json:"prompt"` // 出图用的外观描述（不含画风）
	Voice  string            `json:"voice,omitempty"`
	// Aliases 是剧本里对同一角色的其他称呼（如「林同学」）；台词说话人用主名。
	Aliases []string `json:"aliases,omitempty"`
}

type Shot struct {
	Duration    int      `json:"duration"`
	ShotSize    string   `json:"shot_size"`
	Camera      string   `json:"camera"`
	Action      string   `json:"action"`
	Dialogue    []Line   `json:"dialogue"`
	Characters  []string `json:"characters"`
	Scene       string   `json:"scene"`
	Props       []string `json:"props"`
	ImagePrompt string   `json:"image_prompt"`
	VideoPrompt string   `json:"video_prompt"`
}

type Line struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	// 剧本没写明谁说的：Speaker 留空，SpeakerGuess 是推测，由人确认，不自动分配。
	SpeakerUncertain bool   `json:"speaker_uncertain,omitempty"`
	SpeakerGuess     string `json:"speaker_guess,omitempty"`
}

var assetFields = map[string][]string{
	"characters": {"identity", "age", "gender", "personality", "speech", "appearance", "hair", "outfit", "concept"},
	"scenes":     {"location", "interior", "time", "season", "lighting", "composition", "mood"},
	"props":      {"material", "color", "size", "shape", "texture"},
}

const breakdownSchema = `输出一个 JSON 对象，结构如下（只输出 JSON，不要解释）：
{
  "characters": [{"name": "角色名", "fields": {"identity": "身份", "age": "年龄（数字或描述）", "gender": "性别", "personality": "性格", "speech": "说话方式", "appearance": "五官与体型", "hair": "发型发色", "outfit": "服装", "concept": "设计理念，一两句"}, "prompt": "用于生成全身立绘的外观描述，一段话，不写画风", "voice": "适合的声音，如：青年男声，温和略迟疑", "aliases": ["剧本里对这个角色的其他称呼，没有就空数组"]}],
  "scenes": [{"name": "场景名", "fields": {"location": "地点", "interior": "内景或外景", "time": "时间", "season": "季节", "lighting": "光线", "composition": "构图", "mood": "氛围"}, "prompt": "用于生成空镜场景图的描述，不出现人物，不写画风"}],
  "props": [{"name": "道具名", "fields": {"material": "材质", "color": "颜色", "size": "尺寸", "shape": "形状", "texture": "质感"}, "prompt": "用于生成道具单品图的描述，不写画风"}],
  "shots": [{"duration": 5, "shot_size": "景别（远景/全景/中景/近景/特写）", "camera": "运镜（固定/推/拉/摇/移/跟）", "action": "画面里发生什么", "dialogue": [{"speaker": "角色主名", "text": "台词原文", "speaker_uncertain": false, "speaker_guess": ""}], "characters": ["出场角色名"], "scene": "场景名", "props": ["道具名"], "image_prompt": "这一镜第一帧的静态画面描述", "video_prompt": "这一镜的动态描述：人物动作、表情、运镜，有台词时写明谁说了什么"}]
}`

const breakdownRules = `规则：
1. 只用剧本里的人物、场景、道具，不要新增；剧本明确说「不要」的内容不能出现。
2. 剧本已经写明镜头和时长的，按剧本拆，镜头数和时长保持一致。
3. 每镜时长是 2 到 15 的整数秒。
4. 台词照抄剧本原文；没有台词的镜头 dialogue 为空数组。
5. 剧本写明人物是成年人的，描述里保持成年人的外貌。
6. 所有提示词都用中文，不写画风（画风由平台统一加）。
7. 同一个人在剧本里有几种称呼（如「小林」和「林同学」）时只建一个角色，其他称呼写进 aliases；台词的 speaker 和镜头的 characters 都用主名。
8. 剧本没写明是谁说的台词：speaker 留空，speaker_uncertain 写 true，speaker_guess 写最可能的角色主名；不要替剧本决定说话人。旁白的 speaker 写「旁白」。`

var densityGuide = map[string]string{
	"精简": "镜头尽量少，每镜 5 秒左右，只保留推动情节的画面。",
	"标准": "按情节节拍拆镜头，每镜 3 到 6 秒。",
	"详细": "拆得更细，加入反应镜头和环境镜头，每镜 2 到 4 秒。",
}

type textModel struct {
	base, key string
}

func (p *textModel) Run(ctx context.Context, job Job) (Outcome, error) {
	in := job.Input
	var user strings.Builder
	switch job.Kind {
	case KindParse:
		fmt.Fprintf(&user, "把下面的剧本拆成角色、场景、道具和分镜。\n镜头密度：%s（%s）\n\n剧本：\n%s", in.Density, densityGuide[in.Density], in.Script)
	case KindStoryboard:
		fmt.Fprintf(&user, "资产已经确认，请按这些资产重写分镜和提示词。characters、scenes、props 原样返回已确认的资产名，fields 可以留空。\n镜头密度：%s（%s）\n\n已确认的资产：\n", in.Density, densityGuide[in.Density])
		for _, a := range in.Assets {
			fmt.Fprintf(&user, "- %s：%s %s\n", map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}[a.Kind], a.Name, a.Description)
		}
		if len(in.Shots) > 0 {
			user.WriteString("\n现有镜头（保持数量和时长，除非剧本明显需要调整）：\n")
			for i, s := range in.Shots {
				fmt.Fprintf(&user, "%d. %d 秒：%s %s\n", i+1, s.Duration, s.Action, s.Dialogue)
			}
		}
		fmt.Fprintf(&user, "\n剧本：\n%s", in.Script)
	default:
		return Outcome{}, failure("UNSUPPORTED", "这个模型不能做这一步")
	}
	body := map[string]any{
		"model": job.Model.remote, "temperature": 0.4, "max_tokens": 8192, "response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{"role": "system", "content": "你是漫剧的编剧和分镜助手。" + breakdownSchema + "\n" + breakdownRules},
			{"role": "user", "content": user.String()},
		},
	}
	// M3 supports direct answers; structured breakdowns do not need thinking tokens.
	if job.Model.remote == "MiniMax-M3" {
		body["thinking"] = map[string]string{"type": "disabled"}
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := call(ctx, "text", "POST", p.base+"/chat/completions", p.key, body, &out); err != nil {
		return Outcome{}, err
	}
	if len(out.Choices) == 0 {
		return Outcome{}, failure("PARSE_FAILED", "模型没有给出结果，请重试")
	}
	if out.Choices[0].FinishReason == "length" {
		return Outcome{}, failure("PARSE_TOO_LONG", "剧本拆出的内容太多，模型没写完。请把镜头密度调成「精简」，或把剧本分成两集再解析")
	}
	b, err := readBreakdown(out.Choices[0].Message.Content)
	if err != nil {
		slog.Warn("comic breakdown unreadable", "service", "edu-agent-jiuwen-server", "finish", out.Choices[0].FinishReason, "length", len(out.Choices[0].Message.Content), "head", clip(out.Choices[0].Message.Content, 200))
		return Outcome{}, err
	}
	return Outcome{Result: b}, nil
}

func (p *textModel) Check(context.Context, Job) (Outcome, error) { return Outcome{}, errNotAsync }

// readBreakdown is lenient about scalars: models write "age": 20 or "duration": "5秒" as often as strings.
func readBreakdown(content string) (Breakdown, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(strings.TrimPrefix(content, "```json"), "```")
	content = strings.TrimSuffix(content, "```")
	var raw any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return Breakdown{}, failure("PARSE_FAILED", "模型的拆解结果格式不对，请重试；剧本正文不受影响")
	}
	var b Breakdown
	if err := json.Unmarshal(loosen(raw, ""), &b); err != nil {
		return b, failure("PARSE_FAILED", "模型的拆解结果格式不对，请重试；剧本正文不受影响")
	}
	b.normalize()
	if len(b.Shots) == 0 {
		return b, failure("PARSE_FAILED", "没有拆出镜头。请检查剧本是否写了画面和动作，再重试")
	}
	return b, nil
}

func clip(s string, limit int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > limit {
		return string([]rune(s)[:limit])
	}
	return s
}

// normalize trims and caps whatever the model returned and drops references to unknown names.
func (b *Breakdown) normalize() {
	fix := func(list []Asset, group string, limit int) []Asset {
		var out []Asset
		seen := map[string]bool{}
		for _, a := range list {
			a.Name = clip(a.Name, 20)
			if a.Name == "" || seen[a.Name] || len(out) >= limit {
				continue
			}
			seen[a.Name] = true
			fields := map[string]string{}
			for _, key := range assetFields[group] {
				if v := clip(a.Fields[key], 300); v != "" {
					fields[key] = v
				}
			}
			a.Fields, a.Prompt, a.Voice = fields, clip(a.Prompt, 1000), clip(a.Voice, 60)
			var aliases []string
			for _, n := range a.Aliases {
				if n = clip(n, 20); n != "" && n != a.Name && !slices.Contains(aliases, n) && len(aliases) < 5 {
					aliases = append(aliases, n)
				}
			}
			a.Aliases = aliases
			out = append(out, a)
		}
		return out
	}
	b.Characters = fix(b.Characters, "characters", 12)
	b.Scenes = fix(b.Scenes, "scenes", 12)
	b.Props = fix(b.Props, "props", 20)
	names := func(list []Asset) []string {
		var out []string
		for _, a := range list {
			out = append(out, a.Name)
		}
		return out
	}
	characters, scenes, props := names(b.Characters), names(b.Scenes), names(b.Props)
	// 别名 → 主名：模型偶尔在台词和出场里用别名，统一成主名。别名和别的角色重名时以角色为准。
	canonical := map[string]string{}
	for _, c := range b.Characters {
		for _, n := range c.Aliases {
			if !slices.Contains(characters, n) {
				canonical[n] = c.Name
			}
		}
	}
	canon := func(n string) string {
		n = strings.TrimSpace(n)
		if c, ok := canonical[n]; ok {
			return c
		}
		return n
	}
	keep := func(list []string, known []string) []string {
		out := []string{}
		for _, n := range list {
			if n = canon(n); slices.Contains(known, n) && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
		return out
	}
	var shots []Shot
	for _, s := range b.Shots {
		if len(shots) >= 40 {
			break
		}
		s.Duration = min(max(s.Duration, 2), 15)
		s.ShotSize, s.Camera, s.Action = clip(s.ShotSize, 10), clip(s.Camera, 20), clip(s.Action, 500)
		s.ImagePrompt, s.VideoPrompt = clip(s.ImagePrompt, 1500), clip(s.VideoPrompt, 1500)
		s.Characters, s.Props = keep(s.Characters, characters), keep(s.Props, props)
		if !slices.Contains(scenes, strings.TrimSpace(s.Scene)) {
			s.Scene = ""
		}
		lines := []Line{}
		for _, l := range s.Dialogue {
			if l.Text = clip(l.Text, 300); l.Text != "" {
				l.Speaker, l.SpeakerGuess = canon(l.Speaker), canon(l.SpeakerGuess)
				if l.SpeakerUncertain || l.Speaker == "" && l.SpeakerGuess != "" {
					// 没写明说话人：不分配，只留认得出的推测。
					l.SpeakerUncertain, l.Speaker = true, ""
				}
				if !slices.Contains(characters, l.SpeakerGuess) {
					l.SpeakerGuess = ""
				}
				lines = append(lines, l)
			}
		}
		s.Dialogue = lines
		if s.Action == "" && s.VideoPrompt == "" {
			continue
		}
		shots = append(shots, s)
	}
	b.Shots = shots
}

var leadingInt = regexp.MustCompile(`\d+`)

// loosen rewrites scalars to the types Breakdown expects: duration becomes a number, every other scalar a string.
func loosen(v any, key string) []byte {
	var walk func(v any, key string) any
	walk = func(v any, key string) any {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				t[k] = walk(x, k)
			}
			return t
		case []any:
			for i, x := range t {
				t[i] = walk(x, "")
			}
			return t
		case nil:
			return nil
		}
		text := strings.TrimSpace(fmt.Sprint(v))
		if key == "duration" {
			n, _ := strconv.Atoi(leadingInt.FindString(text))
			return n
		}
		if key == "speaker_uncertain" {
			return text == "true" || text == "1" || text == "是"
		}
		return text
	}
	out, _ := json.Marshal(walk(v, key))
	return out
}
