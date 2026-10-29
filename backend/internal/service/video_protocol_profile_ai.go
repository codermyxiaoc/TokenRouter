package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
)

// SettingKeyVideoProfileAI 保存视频协议模板 AI 生成的模型配置（OpenAI 兼容 Chat Completions）。
const SettingKeyVideoProfileAI = "video_profile_ai_config"

const (
	// 上游文档上限，避免一次请求占用过多模型上下文与内存。
	videoProfileAIMaxDocumentRunes = 120000
	// 生成后按模板校验结果最多修正的轮数（含首轮）。
	videoProfileAIMaxAttempts      = 3
	defaultVideoProfileAITimeout   = 120
	maxVideoProfileAITimeout       = 600
	videoProfileAIMaxResponseBytes = 2 << 20
)

// VideoProfileAIConfig 是保存的 AI 配置；APIKey 只在服务端使用，不回显给前端。
// @project-doc docs/interfaces/video_upstream.md#video_protocol_ai
type VideoProfileAIConfig struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key,omitempty"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// VideoProfileAIConfigView 是返回给管理端的配置，只说明密钥是否已配置。
type VideoProfileAIConfigView struct {
	Enabled          bool   `json:"enabled"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	APIKeyConfigured bool   `json:"api_key_configured"`
}

// VideoProfileAIConfigInput 更新配置；APIKey 为空表示保留原密钥，ClearAPIKey 显式清除。
type VideoProfileAIConfigInput struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	ClearAPIKey    bool   `json:"clear_api_key"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// VideoProfileAIGenerateInput 是生成请求：上游文档原文与管理员补充说明。
type VideoProfileAIGenerateInput struct {
	Document     string `json:"document"`
	Instructions string `json:"instructions"`
	// BaseSpec 可选：在已有模板基础上按文档修改。
	BaseSpec json.RawMessage `json:"base_spec,omitempty"`
}

// VideoProfileAIGenerateResult 是生成结果；校验未通过时仍返回最后一版草稿供管理员修改。
type VideoProfileAIGenerateResult struct {
	ProfileKey      string          `json:"profile_key"`
	Name            string          `json:"name"`
	Spec            json.RawMessage `json:"spec"`
	Notes           string          `json:"notes"`
	Valid           bool            `json:"valid"`
	ValidationError string          `json:"validation_error,omitempty"`
	Attempts        int             `json:"attempts"`
}

// VideoProfileAIService 用管理员配置的模型把上游文档转换为声明式协议模板草稿；生成结果只回填编辑器，不会自动保存或启用。
type VideoProfileAIService struct {
	settings SettingRepository
	client   *http.Client
}

// NewVideoProfileAIService 创建 AI 模板生成服务。
func NewVideoProfileAIService(settings SettingRepository) *VideoProfileAIService {
	return &VideoProfileAIService{settings: settings, client: &http.Client{}}
}

func (s *VideoProfileAIService) loadConfig(ctx context.Context) (VideoProfileAIConfig, error) {
	var cfg VideoProfileAIConfig
	if s == nil || s.settings == nil {
		return cfg, infraerrors.ServiceUnavailable("VIDEO_PROFILE_AI_UNAVAILABLE", "Video profile AI settings are unavailable")
	}
	raw, err := s.settings.GetValue(ctx, SettingKeyVideoProfileAI)
	if errors.Is(err, ErrSettingNotFound) || err == nil && strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return VideoProfileAIConfig{}, nil
	}
	return cfg, nil
}

// GetConfig 返回不含密钥的配置。
func (s *VideoProfileAIService) GetConfig(ctx context.Context) (*VideoProfileAIConfigView, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = defaultVideoProfileAITimeout
	}
	return &VideoProfileAIConfigView{Enabled: cfg.Enabled, BaseURL: cfg.BaseURL, Model: cfg.Model, TimeoutSeconds: cfg.TimeoutSeconds, APIKeyConfigured: cfg.APIKey != ""}, nil
}

// UpdateConfig 校验并保存配置；启用时必须具备地址、模型与密钥。
func (s *VideoProfileAIService) UpdateConfig(ctx context.Context, input VideoProfileAIConfigInput) (*VideoProfileAIConfigView, error) {
	current, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	invalid := func(message string) error { return infraerrors.BadRequest("VIDEO_PROFILE_AI_INVALID", message) }
	next := VideoProfileAIConfig{Enabled: input.Enabled, BaseURL: strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"), Model: strings.TrimSpace(input.Model), TimeoutSeconds: input.TimeoutSeconds, APIKey: current.APIKey}
	if key := strings.TrimSpace(input.APIKey); key != "" {
		next.APIKey = key
	} else if input.ClearAPIKey {
		next.APIKey = ""
	}
	if next.TimeoutSeconds == 0 {
		next.TimeoutSeconds = defaultVideoProfileAITimeout
	}
	if next.TimeoutSeconds < 10 || next.TimeoutSeconds > maxVideoProfileAITimeout {
		return nil, invalid("timeout_seconds must be between 10 and 600")
	}
	if next.BaseURL != "" {
		if _, err := videoProfileAIEndpoint(next.BaseURL); err != nil {
			return nil, invalid(err.Error())
		}
	}
	if len(next.Model) > 200 || strings.ContainsAny(next.Model, "\r\n") || strings.ContainsAny(next.APIKey, "\r\n") {
		return nil, invalid("model or api_key contains invalid characters")
	}
	if next.Enabled && (next.BaseURL == "" || next.Model == "" || next.APIKey == "") {
		return nil, invalid("base_url, model and api_key are required when AI generation is enabled")
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	if err := s.settings.Set(ctx, SettingKeyVideoProfileAI, string(raw)); err != nil {
		return nil, err
	}
	return &VideoProfileAIConfigView{Enabled: next.Enabled, BaseURL: next.BaseURL, Model: next.Model, TimeoutSeconds: next.TimeoutSeconds, APIKeyConfigured: next.APIKey != ""}, nil
}

// videoProfileAIEndpoint 接受 OpenAI 兼容的 Base URL（如 https://api.example.com/v1）或完整 chat/completions 地址。
func videoProfileAIEndpoint(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("base_url must be an HTTP(S) URL without credentials, query or fragment")
	}
	if strings.HasSuffix(u.Path, "/chat/completions") {
		return u.String(), nil
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return u.String(), nil
}

// Generate 把上游文档交给模型生成模板，并用模板保存校验逐轮反馈错误，最多修正 videoProfileAIMaxAttempts 轮。
func (s *VideoProfileAIService) Generate(ctx context.Context, input VideoProfileAIGenerateInput) (*VideoProfileAIGenerateResult, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || cfg.BaseURL == "" || cfg.Model == "" || cfg.APIKey == "" {
		return nil, infraerrors.BadRequest("VIDEO_PROFILE_AI_DISABLED", "Configure and enable video protocol template AI in system settings first")
	}
	document := strings.TrimSpace(input.Document)
	if document == "" {
		return nil, infraerrors.BadRequest("VIDEO_PROFILE_AI_INVALID", "Upstream document is required")
	}
	if len([]rune(document)) > videoProfileAIMaxDocumentRunes || len([]rune(input.Instructions)) > 4000 {
		return nil, infraerrors.BadRequest("VIDEO_PROFILE_AI_INVALID", "Upstream document or instructions are too long")
	}
	if len(input.BaseSpec) > 0 && !json.Valid(input.BaseSpec) {
		return nil, infraerrors.BadRequest("VIDEO_PROFILE_AI_INVALID", "base_spec must be valid JSON")
	}
	endpoint, err := videoProfileAIEndpoint(cfg.BaseURL)
	if err != nil {
		return nil, infraerrors.BadRequest("VIDEO_PROFILE_AI_INVALID", err.Error())
	}
	timeout := cfg.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultVideoProfileAITimeout
	}
	messages := []map[string]string{
		{"role": "system", "content": videoProfileAISystemPrompt()},
		{"role": "user", "content": videoProfileAIUserPrompt(document, input.Instructions, input.BaseSpec)},
	}
	result := &VideoProfileAIGenerateResult{}
	for attempt := 1; attempt <= videoProfileAIMaxAttempts; attempt++ {
		result.Attempts = attempt
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		content, callErr := s.chat(callCtx, endpoint, cfg, messages)
		cancel()
		if callErr != nil {
			return nil, callErr
		}
		draft, parseErr := parseVideoProfileAIDraft(content)
		if parseErr != nil {
			result.Valid, result.ValidationError = false, parseErr.Error()
			messages = append(messages, map[string]string{"role": "assistant", "content": content},
				map[string]string{"role": "user", "content": "输出无法解析为要求的 JSON 对象：" + parseErr.Error() + "。请只输出一个完整 JSON 对象，不要使用 Markdown。"})
			continue
		}
		result.ProfileKey, result.Name, result.Notes, result.Spec = draft.ProfileKey, draft.Name, draft.Notes, draft.Spec
		var spec VideoProtocolProfileSpec
		decoder := json.NewDecoder(bytes.NewReader(draft.Spec))
		decoder.DisallowUnknownFields()
		validateErr := decoder.Decode(&spec)
		if validateErr == nil {
			validateErr = spec.Validate()
		}
		if validateErr == nil {
			// 用规范化后的 JSON 回填编辑器，去掉模型输出中的多余空白与未知字段。
			if normalized, marshalErr := json.Marshal(spec); marshalErr == nil {
				result.Spec = normalized
			}
			result.Valid, result.ValidationError = true, ""
			if !ValidVideoProfileKey(result.ProfileKey) {
				result.ProfileKey = ""
			}
			return result, nil
		}
		result.Valid, result.ValidationError = false, infraerrors.Message(validateErr)
		if result.ValidationError == "" {
			result.ValidationError = validateErr.Error()
		}
		messages = append(messages, map[string]string{"role": "assistant", "content": content},
			map[string]string{"role": "user", "content": "模板校验失败：" + result.ValidationError + "。请修正 spec 并重新输出完整 JSON 对象，保持其它正确部分不变。"})
	}
	if !ValidVideoProfileKey(result.ProfileKey) {
		result.ProfileKey = ""
	}
	return result, nil
}

// chat 调用 OpenAI 兼容的 Chat Completions，返回第一条回复正文；错误不回显密钥。
func (s *VideoProfileAIService) chat(ctx context.Context, endpoint string, cfg VideoProfileAIConfig, messages []map[string]string) (string, error) {
	payload, err := json.Marshal(map[string]any{"model": cfg.Model, "messages": messages, "temperature": 0.1})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", infraerrors.New(http.StatusBadGateway, "VIDEO_PROFILE_AI_UPSTREAM", "AI model request failed: "+sanitizeVideoProfileAIError(err.Error(), cfg.APIKey))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, videoProfileAIMaxResponseBytes))
	if err != nil {
		return "", infraerrors.New(http.StatusBadGateway, "VIDEO_PROFILE_AI_UPSTREAM", "AI model response could not be read")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := firstVideoString(gjson.ParseBytes(body), "error.message", "message", "error")
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return "", infraerrors.New(http.StatusBadGateway, "VIDEO_PROFILE_AI_UPSTREAM", fmt.Sprintf("AI model returned HTTP %d: %s", resp.StatusCode, sanitizeVideoProfileAIError(message, cfg.APIKey)))
	}
	content := gjson.GetBytes(body, "choices.0.message.content").String()
	if strings.TrimSpace(content) == "" {
		return "", infraerrors.New(http.StatusBadGateway, "VIDEO_PROFILE_AI_UPSTREAM", "AI model returned an empty response")
	}
	return content, nil
}

// sanitizeVideoProfileAIError 截断并遮罩错误信息中的密钥。
func sanitizeVideoProfileAIError(message, key string) string {
	if key != "" {
		message = strings.ReplaceAll(message, key, "***")
	}
	if runes := []rune(message); len(runes) > 300 {
		message = string(runes[:300])
	}
	return message
}

type videoProfileAIDraft struct {
	ProfileKey string          `json:"profile_key"`
	Name       string          `json:"name"`
	Notes      string          `json:"notes"`
	Spec       json.RawMessage `json:"spec"`
}

// parseVideoProfileAIDraft 从回复中取出 JSON 对象：允许模型包裹 Markdown 代码块或前后附带说明。
func parseVideoProfileAIDraft(content string) (*videoProfileAIDraft, error) {
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object found")
	}
	raw := content[start : end+1]
	if !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("invalid JSON")
	}
	draft := &videoProfileAIDraft{}
	if err := json.Unmarshal([]byte(raw), draft); err != nil {
		return nil, err
	}
	if len(draft.Spec) == 0 || !gjson.ParseBytes(draft.Spec).IsObject() {
		// 模型直接输出 spec 本身时也接受。
		if gjson.Get(raw, "create").IsObject() && gjson.Get(raw, "poll").IsObject() {
			draft.Spec = json.RawMessage(raw)
		} else {
			return nil, fmt.Errorf("spec object is missing")
		}
	}
	draft.ProfileKey = strings.TrimSpace(draft.ProfileKey)
	draft.Name = strings.TrimSpace(draft.Name)
	return draft, nil
}

// videoProfileAIUserPrompt 组装用户消息：上游文档、补充说明与可选的基础模板。
func videoProfileAIUserPrompt(document, instructions string, base json.RawMessage) string {
	var b strings.Builder
	b.WriteString("请根据以下上游视频接口文档生成协议模板。\n")
	if strings.TrimSpace(instructions) != "" {
		b.WriteString("\n## 管理员补充说明\n")
		b.WriteString(strings.TrimSpace(instructions))
		b.WriteString("\n")
	}
	if len(base) > 0 {
		b.WriteString("\n## 在此模板基础上修改\n")
		b.Write(base)
		b.WriteString("\n")
	}
	b.WriteString("\n## 上游文档\n")
	b.WriteString(document)
	return b.String()
}

// videoProfileAISystemPrompt 描述模板规格与约束，并附带预置模板作为范例，范例随内置定义自动更新。
func videoProfileAISystemPrompt() string {
	examples := []string{}
	for _, preset := range videoProtocolPresets() {
		switch preset.Key {
		case "generic-flat", "yezonghui", "kemei-kling-omni", "kemei-minimax":
			raw, _ := json.Marshal(preset.Spec)
			examples = append(examples, "### "+preset.Key+"（"+preset.Name+"）\n"+string(raw))
		}
	}
	return `你是视频网关的协议模板工程师。网关对客户端只提供统一请求，你要把上游视频接口文档转换为“声明式协议模板”JSON，网关据此把统一请求转换为上游请求并解析响应。

只输出一个 JSON 对象，不要 Markdown，格式：
{"profile_key":"小写字母数字下划线或连字符，≤64","name":"中文名称","notes":"给管理员的说明：映射依据、文档中不确定或无法表达的地方","spec":{...}}

## 统一请求字段（规则 from 可读取）
model, prompt, negative_prompt, duration(数字秒), resolution(如 720p/1080p/2k), aspect_ratio(如 16:9/adaptive), size(像素，如 1280x720), generate_audio(bool), watermark(bool), seed(int),
images（列表，元素 {url, role}，role 取 first_frame/last_frame/reference_image，可能为空）, videos（列表，role 为 reference_video）, images[0], images[role=first_frame], extra.<key>（厂商扩展）。
统一请求中非空但没有被任何规则读取的字段会被拒绝，所以文档支持的字段都要映射；文档不支持的不要映射。

## spec 结构
- auth: {"scheme":"bearer"|"token"|"header","header":"仅 header 时填写"}；密钥来自账号，禁止写入模板。
- headers: 所有请求附带的静态请求头（禁止 Authorization/Host/Cookie/Content-Type 等）。
- capabilities（可选）: durations[], min_duration, max_duration, integer_duration, resolutions[], aspect_ratios[], max_images, min_images, image_roles[], max_videos, exclusive_frames, require_ratio_without_media, deny_models[], public_media, size_resolutions{像素:档位}。只写文档明确的限制。
- create: {"path":"以 / 开头的相对路径，可含 {model}","headers":{仅创建请求},"defaults":{"duration":5,"resolution":"720p","aspect_ratio":"16:9"},"image_role_default":"frames|single_first|reference","body":[规则...],"response":{"task_id":["JSON 路径"...],"refs":{"名称":"路径"},"rejected_when":[条件]}}
- 规则字段：from 或 from_all（多个标量联合查表，map 键为值用 | 连接）或 const；to（点分 JSON 路径，数组下标用数字）；type: string|int|number|bool|string_number|string_int|resolution|resolution_upper|resolution_tier|ratio；map（取值映射，未命中即拒绝）；each（媒体列表生成对象数组，值用 $url/$role；标量用 $value 包装成对象）；item（媒体列表生成字符串数组，"$url"）；roles（角色改名）；append（追加到数组）；required；default（来源缺省时写入的 JSON 值）。
- poll: {"path":"可含 {task_id} {model} {ref.名称}","query":{"参数":"{task_id}"},"interval_seconds":秒,"max_processing_minutes":分钟,"response":{...}}
- poll.response: root（候选根路径数组）, list（{"path","match":[ID 路径],"optional":true}，响应为任务数组时使用）, task_id[], status（路径数组）, status_map（上游状态 → queued|processing|completed|failed|cancelled|expired，至少含 completed 和 failed）, video_url[], seconds[], tokens[], resolution[], outputs（{"path","max":1,"filter":{},"url":[],"seconds":[]}）, completed_invalid_when[条件], strict, public_url。
- 条件：{"path":"...","in":[...]} 或 {"path":"...","not_in":[...]} 或 {"path":"...","exists":true}。
- content: {"mode":"url"}（成品是直链，需 video_url）或 {"mode":"path","path":"/.../{task_id}/content"}（需原账号凭据下载）。

## 约束
- 路径只能是相对路径，不得包含主机、查询串或 ..；JSON 路径只用点分和数字下标，不得使用 gjson 查询语法。
- 状态值保持文档原文大小写，status_map 不区分大小写。
- 时长、分辨率、比例的计价维度来自被映射的字段，必须与实际发送值一致；不要编造文档没有的字段。
- 不确定的地方写进 notes，而不是猜测。

## 范例（系统预置模板）
` + strings.Join(examples, "\n\n")
}
