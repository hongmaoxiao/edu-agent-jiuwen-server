package comic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Job is one task as a provider sees it: the model, the validated input and the local files it refers to.
type Job struct {
	TaskID         string
	Kind           string
	Model          *Model
	Input          Input
	ProviderTaskID string
	Files          map[string]File // media id → file on disk (refs, first frame, compose inputs)
	Work           string          // scratch directory, removed after the step
}

// File is a stored media file a job may read.
type File struct {
	ID         string
	Path       string
	Kind       string
	Mime       string
	DurationMS int
	HasAudio   bool
	Width      int
	Height     int
}

// Produced is one output file. Either Data or Path is set; Path must be inside Job.Work.
type Produced struct {
	Data []byte
	Path string
	Ext  string
	Kind string // image | audio | video
	Meta map[string]any
}

// Outcome of one step. Pending means the provider accepted an asynchronous job; check again later.
type Outcome struct {
	Pending        bool
	ProviderTaskID string
	Files          []Produced
	Failed         int    // items that did not come out (images), for a partial result and refund
	FailedReason   string // why those items failed
	Result         any    // parse / storyboard / voice results
}

type Provider interface {
	// Run starts a task. Synchronous models finish here; asynchronous ones return Pending.
	Run(ctx context.Context, job Job) (Outcome, error)
	// Check polls an asynchronous task.
	Check(ctx context.Context, job Job) (Outcome, error)
}

// Failure is a provider error rewritten for the student: what happened and what to do next.
type Failure struct {
	Code    string
	Message string
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

func failure(code, message string) *Failure { return &Failure{Code: code, Message: message} }

var codeInMessage = regexp.MustCompile(`\((\d{4})\)\s*$`)

// explain turns a provider's code or message into a human reason with a next step.
func explain(provider string, status int, code, message string) *Failure {
	if code == "" {
		if m := codeInMessage.FindStringSubmatch(message); m != nil {
			code = m[1]
		}
	}
	lower := strings.ToLower(code + " " + message)
	switch {
	case status == 401 || status == 403 || code == "1004" || strings.Contains(lower, "authenticationerror") || strings.Contains(lower, "invalid api key"):
		return failure("PROVIDER_AUTH", "平台的模型密钥无效或已过期，请联系老师；创作点已退回")
	case status == 402 || code == "1008" || strings.Contains(lower, "insufficient") || strings.Contains(lower, "accountoverdue"):
		return failure("PROVIDER_BALANCE", "平台的模型账户余额不足，请联系老师；创作点已退回")
	case code == "1026" || code == "1027" || strings.Contains(lower, "sensitive") || strings.Contains(lower, "risk") || strings.Contains(lower, "safety"):
		return failure("CONTENT_REJECTED", "内容没有通过模型的安全审核。请改写提示词（避免敏感词、真实人名），或换一张参考图再试")
	case status == 429 || code == "1002" || code == "1039" || strings.Contains(lower, "ratelimit") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "overloaded") || status == 529:
		return failure("PROVIDER_BUSY", "模型现在太忙，请稍后重试，或换一个模型")
	case status == 400 || status == 422 || code == "2013" || strings.Contains(lower, "invalidparameter") || strings.Contains(lower, "invalid param"):
		return failure("PROVIDER_REJECTED", "模型不接受这组参数或素材（"+short(message)+"）。可以调整参数、换参考图，或换一个模型")
	case code == "1001" || strings.Contains(lower, "timeout"):
		return failure("PROVIDER_TIMEOUT", "模型生成超时，请重试")
	}
	return failure("PROVIDER_ERROR", "模型服务出错（"+short(message)+"），请重试或换一个模型")
}

func short(message string) string {
	message = strings.TrimSpace(message)
	if r := []rune(message); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	if message == "" {
		return "没有说明原因"
	}
	return message
}

// client is shared by the providers; the server's HTTPS_PROXY applies (Grok reaches providers through it).
var client = &http.Client{Timeout: 11 * time.Minute}

// ResultUnknown: the request may have reached the provider (and be billed) but no usable answer came back.
// 用户 2026-11-17 定：这种任务点数先冻结、标「待核对」，核对后确实发出就扣，没发出就退。
const ResultUnknown = "RESULT_UNKNOWN"

// call posts or gets JSON. On HTTP errors it extracts the provider's error and explains it.
func call(ctx context.Context, provider, method, url, key string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 连接都没建立（含代理连不上）：请求肯定没发出。其余（超时、发出后断开）不知道模型收没收到。
		var op *net.OpError
		if errors.As(err, &op) && (op.Op == "dial" || op.Op == "proxyconnect") {
			return failure("PROVIDER_UNREACHABLE", "连不上模型服务，请稍后重试")
		}
		return failure(ResultUnknown, "请求可能已经发给模型，但没收到回复")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return failure(ResultUnknown, "模型服务的回复没有收完整")
	}
	if resp.StatusCode >= 300 {
		code, message := errorOf(raw)
		return explain(provider, resp.StatusCode, code, message)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return failure(ResultUnknown, "模型服务接受了请求，但回复看不懂")
		}
	}
	return nil
}

// errorOf reads {error:{code,message}}, {error:{type,message}} and MiniMax's {base_resp:{status_code,status_msg}}.
func errorOf(raw []byte) (string, string) {
	var e struct {
		Error *struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		BaseResp *struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.Error != nil {
			code := fmt.Sprint(e.Error.Code)
			if e.Error.Code == nil {
				code = ""
			}
			return code, e.Error.Message
		}
		if e.BaseResp != nil {
			return fmt.Sprint(e.BaseResp.StatusCode), e.BaseResp.StatusMsg
		}
	}
	return "", string(raw[:min(len(raw), 200)])
}

// baseErr checks MiniMax's v1 envelope, which reports failures with HTTP 200.
func baseErr(provider string, b *baseResp) error {
	if b != nil && b.StatusCode != 0 {
		return explain(provider, 200, fmt.Sprint(b.StatusCode), b.StatusMsg)
	}
	return nil
}

type baseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

// download fetches a result URL (provider links expire after about 24 hours, so results are saved at once).
func download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, failure("DOWNLOAD_FAILED", "生成结果下载失败，请重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, failure("DOWNLOAD_FAILED", fmt.Sprintf("生成结果下载失败（%d），请重试", resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, failure("DOWNLOAD_FAILED", "生成结果下载失败或文件过大，请重试")
	}
	return data, nil
}

// dataURI inlines a local file for a provider request (both providers accept data URIs up to a 64 MB body).
func dataURI(f File) (string, error) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return "", err
	}
	return "data:" + f.Mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// refImages loads reference images, downscaled so a request stays well under the body limits.
func refImages(ctx context.Context, job Job, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		f, ok := job.Files[id]
		if !ok || f.Kind != "image" {
			return nil, failure("REF_MISSING", "参考图找不到了，请重新选择")
		}
		small, err := shrinkImage(ctx, f, job.Work)
		if err != nil {
			return nil, err
		}
		uri, err := dataURI(small)
		if err != nil {
			return nil, err
		}
		out = append(out, uri)
	}
	return out, nil
}

var errNotAsync = errors.New("not an asynchronous task")
