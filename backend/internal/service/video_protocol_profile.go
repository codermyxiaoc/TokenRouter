package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// VideoProtocolProfile 是管理员维护的声明式视频协议模板；每次保存生成新版本，已受理任务冻结整份快照。
// @project-doc docs/interfaces/video_upstream.md#video_protocol_profiles
type VideoProtocolProfile struct {
	ID         int64                    `json:"id"`
	ProfileKey string                   `json:"profile_key"`
	Version    int                      `json:"version"`
	Name       string                   `json:"name"`
	Status     string                   `json:"status"`
	Spec       VideoProtocolProfileSpec `json:"spec"`
	SpecHash   string                   `json:"spec_hash"`
	CreatedBy  *int64                   `json:"created_by,omitempty"`
	CreatedAt  time.Time                `json:"created_at"`
}

const (
	VideoProtocolProfileDraft    = "draft"
	VideoProtocolProfileActive   = "active"
	VideoProtocolProfileDisabled = "disabled"
)

// VideoProtocolProfileSpec 描述一个上游：鉴权、能力、创建、查询与内容获取；不支持脚本或任意主机。
type VideoProtocolProfileSpec struct {
	Auth         VideoProfileAuth          `json:"auth"`
	Headers      map[string]string         `json:"headers,omitempty"`
	Capabilities *VideoProfileCapabilities `json:"capabilities,omitempty"`
	Create       VideoProfileCreate        `json:"create"`
	Poll         VideoProfilePoll          `json:"poll"`
	Content      VideoProfileContent       `json:"content"`
}

// VideoProfileAuth 只引用账号自身的 api_key，不允许模板携带凭据。
type VideoProfileAuth struct {
	// Scheme 取 bearer（Authorization: Bearer）、token（Authorization: Token）或 header（自定义请求头原样携带）。
	Scheme string `json:"scheme"`
	Header string `json:"header,omitempty"`
}

// VideoProfileCapabilities 声明上游真实支持的参数范围，画布目录和提交校验共用。
type VideoProfileCapabilities struct {
	Durations    []float64 `json:"durations,omitempty"`
	Resolutions  []string  `json:"resolutions,omitempty"`
	AspectRatios []string  `json:"aspect_ratios,omitempty"`
	MaxImages    int       `json:"max_images,omitempty"`
	ImageRoles   []string  `json:"image_roles,omitempty"`
	MaxVideos    int       `json:"max_videos,omitempty"`
	// MinImages 要求的最少参考图数量，用于必须带图的操作。
	MinImages int `json:"min_images,omitempty"`
	// MinDuration、MaxDuration 限制秒数区间；IntegerDuration 要求整数秒。
	MinDuration     float64 `json:"min_duration,omitempty"`
	MaxDuration     float64 `json:"max_duration,omitempty"`
	IntegerDuration bool    `json:"integer_duration,omitempty"`
	// ExclusiveFrames 表示首尾帧不能与参考图混用，尾帧必须搭配首帧，首尾帧各至多一张。
	ExclusiveFrames bool `json:"exclusive_frames,omitempty"`
	// RequireRatioWithoutMedia 表示不带参考素材（文生视频）时必须给出非 adaptive 的比例。
	RequireRatioWithoutMedia bool `json:"require_ratio_without_media,omitempty"`
	// DenyModels 列出不能使用该模板的上游模型；以 * 结尾表示前缀匹配，大小写不敏感。
	DenyModels []string `json:"deny_models,omitempty"`
	// PublicMedia 表示上游只接受公网 HTTP(S) 素材；云画布会先把内部图片发布为短期公开链接。
	PublicMedia bool `json:"public_media,omitempty"`
	// MaxReferenceBytes 限制云画布单张参考图的字节数，0 表示不限制。
	MaxReferenceBytes int64 `json:"max_reference_bytes,omitempty"`
	// AdaptiveRatio 决定云画布 adaptive 比例的处理：orientation 按首图方向换算为 16:9/9:16，omit 不发送比例。
	AdaptiveRatio string `json:"adaptive_ratio,omitempty"`
	// SizeResolutions 把上游像素尺寸换算为计价分辨率档位，请求与响应共用。
	SizeResolutions map[string]string `json:"size_resolutions,omitempty"`
}

// VideoProfileCreate 描述创建任务的请求与响应。
type VideoProfileCreate struct {
	Path string `json:"path"`
	// Headers 只随创建请求发送的静态请求头（如万相异步开关），规则与全局 headers 相同。
	Headers map[string]string `json:"headers,omitempty"`
	// Defaults 在映射前为缺省的规范字段补值，保证发送值与计价维度一致。
	Defaults VideoProfileDefaults `json:"defaults,omitempty"`
	// ImageRoleDefault 为未写角色的参考图补角色：frames 表示一张首帧、两张首尾帧、三张以上参考图；
	// single_first 表示仅一张时为首帧、其余为参考图；reference 表示全部为参考图；空值保持无角色。
	ImageRoleDefault string                     `json:"image_role_default,omitempty"`
	Body             []VideoProfileRule         `json:"body"`
	Response         VideoProfileCreateResponse `json:"response"`
}

// VideoProfileDefaults 只能为计价相关的规范字段提供默认值。
type VideoProfileDefaults struct {
	Duration    *float64 `json:"duration,omitempty"`
	Resolution  string   `json:"resolution,omitempty"`
	AspectRatio string   `json:"aspect_ratio,omitempty"`
}

// VideoProfileRule 把一个规范字段（或常量）写入上游请求体的指定路径。
type VideoProfileRule struct {
	From string `json:"from,omitempty"`
	// FromAll 联合多个标量字段查表，Map 的键为各字段值用 | 连接；字段须全部给出或全部缺省。
	FromAll []string        `json:"from_all,omitempty"`
	Const   json.RawMessage `json:"const,omitempty"`
	To      string          `json:"to"`
	Type    string          `json:"type,omitempty"`
	Map     map[string]any  `json:"map,omitempty"`
	// Each 渲染对象：媒体列表的每一项（$url、$role），或标量值（$value）；空值字段省略。
	Each map[string]string `json:"each,omitempty"`
	// Item 让媒体列表输出字符串数组，取 $url 或 $role。
	Item string `json:"item,omitempty"`
	// Roles 改写 $role 的取值；设置后未列出的角色一律拒绝。
	Roles map[string]string `json:"roles,omitempty"`
	// Default 在来源缺省时写入的 JSON 值，与同路径的显式值互斥而不冲突。
	Default  json.RawMessage `json:"default,omitempty"`
	Append   bool            `json:"append,omitempty"`
	Required bool            `json:"required,omitempty"`
}

// VideoProfileCreateResponse 描述创建响应中的任务身份与明确未受理条件。
type VideoProfileCreateResponse struct {
	TaskID       []string                `json:"task_id"`
	Refs         map[string]string       `json:"refs,omitempty"`
	RejectedWhen []VideoProfileCondition `json:"rejected_when,omitempty"`
}

// VideoProfileCondition 判断响应中某个字段的取值；只在没有任务 ID 时用于确认未受理。
type VideoProfileCondition struct {
	Path   string `json:"path"`
	In     []any  `json:"in,omitempty"`
	NotIn  []any  `json:"not_in,omitempty"`
	Exists *bool  `json:"exists,omitempty"`
}

// VideoProfilePoll 描述查询任务的请求与响应映射。
type VideoProfilePoll struct {
	Path                 string                   `json:"path"`
	Query                map[string]string        `json:"query,omitempty"`
	IntervalSeconds      int                      `json:"interval_seconds,omitempty"`
	MaxProcessingMinutes int                      `json:"max_processing_minutes,omitempty"`
	Response             VideoProfilePollResponse `json:"response"`
}

// VideoProfilePollResponse 描述查询响应：可选根节点或按任务 ID 匹配的列表项。
type VideoProfilePollResponse struct {
	// Root 依次尝试的根节点，取第一个存在的对象；都不存在时使用整包。
	Root   VideoProfilePaths `json:"root,omitempty"`
	List   *VideoProfileList `json:"list,omitempty"`
	TaskID []string          `json:"task_id,omitempty"`
	// Status 依次尝试的状态路径，取第一个非空字符串。
	Status       VideoProfilePaths `json:"status"`
	StatusMap    map[string]string `json:"status_map"`
	Progress     string            `json:"progress,omitempty"`
	VideoURL     []string          `json:"video_url,omitempty"`
	ErrorMessage []string          `json:"error_message,omitempty"`
	Seconds      []string          `json:"seconds,omitempty"`
	Tokens       []string          `json:"tokens,omitempty"`
	Resolution   []string          `json:"resolution,omitempty"`
	// Outputs 声明成品数组；数组项超过 Max 时无法确定计价对象，标记元数据错误。
	Outputs *VideoProfileOutputs `json:"outputs,omitempty"`
	// CompletedInvalidWhen 在上游声称完成时校验：任一条件命中（如同时返回错误包）即标记元数据错误、不交付。
	CompletedInvalidWhen []VideoProfileCondition `json:"completed_invalid_when,omitempty"`
	// Strict 要求秒数、Token 与分辨率的多个路径取值一致，不一致时标记元数据错误；否则取第一个有效值。直链总是取第一个。
	Strict bool `json:"strict,omitempty"`
	// PublicURL 要求成品直链是公网 HTTP(S) 地址。
	PublicURL bool `json:"public_url,omitempty"`
}

// VideoProfileList 从数组中只取任务 ID 精确匹配的一项，其它任务出现时整包拒绝。
type VideoProfileList struct {
	Path string `json:"path"`
	// Match 依次尝试的列表项任务 ID 路径，取值不一致视为其它任务。
	Match VideoProfilePaths `json:"match"`
	// Optional 表示该路径不是数组时直接使用当前节点，而不是视为无法匹配。
	Optional bool `json:"optional,omitempty"`
}

// VideoProfileOutputs 描述成品数组的位置与允许的最大项数。
type VideoProfileOutputs struct {
	Path string `json:"path"`
	Max  int    `json:"max"`
	// Filter 只统计字段取值全部匹配的数组项，例如 type=video，其它产物（封面、音频）不参与计价。
	Filter map[string]string `json:"filter,omitempty"`
	// URL、Seconds 是数组项内的相对路径；声明后直链与秒数取自唯一匹配项，上游声称完成时两者都必须给出。
	URL     []string `json:"url,omitempty"`
	Seconds []string `json:"seconds,omitempty"`
}

// VideoProfilePaths 是一组 JSON 路径；兼容只写一个字符串的旧模板，单个路径序列化为字符串以保持哈希不变。
type VideoProfilePaths []string

// UnmarshalJSON 同时接受字符串与字符串数组。
func (p *VideoProfilePaths) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*p = nil
		if single != "" {
			*p = VideoProfilePaths{single}
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("JSON path must be a string or an array of strings")
	}
	*p = list
	return nil
}

// MarshalJSON 单个路径输出字符串，多个路径输出数组。
func (p VideoProfilePaths) MarshalJSON() ([]byte, error) {
	if len(p) == 1 {
		return json.Marshal(p[0])
	}
	return json.Marshal([]string(p))
}

// VideoProfileContent 描述成品获取：直链（url）或用原账号凭据请求内容路径（path）；
// path 模式同样读取 video_url，查询给出直链时优先直链，缺失时回退内容路径。
type VideoProfileContent struct {
	Mode string `json:"mode"`
	Path string `json:"path,omitempty"`
}

var (
	videoProfileKeyPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	videoProfileRefPattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	videoProfilePlaceholder      = regexp.MustCompile(`\{([^{}]*)\}`)
	videoProfileForbiddenHeaders = map[string]bool{
		"authorization": true, "host": true, "cookie": true, "content-length": true, "content-type": true,
		"transfer-encoding": true, "connection": true, "proxy-authorization": true, "x-api-key": true,
	}
	// videoProfileSources 列出模板可以读取的规范字段。
	videoProfileScalarSources = map[string]bool{
		"model": true, "prompt": true, "negative_prompt": true, "duration": true, "resolution": true, "aspect_ratio": true,
		"size": true, "generate_audio": true, "watermark": true, "seed": true,
	}
	videoProfileTaskStatuses = map[string]bool{"queued": true, "processing": true, "completed": true, "failed": true, "cancelled": true, "expired": true}
)

const (
	// 模板协议在快照中以该前缀加键名与版本号标识。
	videoProfileProtocolPrefix = "profile:"
	videoTargetVersionProfile  = 3
	// 处理中任务的最长等待；超时后失败并释放预扣，避免无法识别的状态造成永久冻结。
	defaultVideoMaxProcessingMinutes = 180
)

// ValidVideoProfileKey 校验模板键名，只允许小写字母、数字、下划线和连字符。
func ValidVideoProfileKey(key string) bool {
	return videoProfileKeyPattern.MatchString(key)
}

// VideoProfileProtocolID 返回任务快照中的协议标识。
func VideoProfileProtocolID(key string, version int) string {
	return videoProfileProtocolPrefix + key + "@" + strconv.Itoa(version)
}

// ParseVideoProfileReference 解析账号绑定的 profile:key 或 profile:key@version；未写版本表示使用当前启用版本。
func ParseVideoProfileReference(value string) (string, int, bool) {
	rest, ok := strings.CutPrefix(value, videoProfileProtocolPrefix)
	if !ok {
		return "", 0, false
	}
	key, version := rest, 0
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		key = rest[:at]
		n, err := strconv.Atoi(rest[at+1:])
		if err != nil || n <= 0 {
			return "", 0, false
		}
		version = n
	}
	return key, version, ValidVideoProfileKey(key)
}

// Hash 返回规范 JSON 的 sha256，任务快照恢复时据此检查模板未被篡改。
func (spec VideoProtocolProfileSpec) Hash() (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func videoProfileInvalid(format string, args ...any) error {
	return videoCanonicalError("VIDEO_PROFILE_INVALID", fmt.Sprintf(format, args...))
}

// Validate 在保存时拒绝含糊或不安全的模板：非法路径与占位符、敏感请求头、缺少终态映射、引用不存在的字段。
func (spec VideoProtocolProfileSpec) Validate() error {
	switch spec.Auth.Scheme {
	case "bearer", "token":
		if spec.Auth.Header != "" {
			return videoProfileInvalid("auth.header is only used by the header scheme")
		}
	case "header":
		if !validVideoProfileHeaderName(spec.Auth.Header) || videoProfileForbiddenHeaders[strings.ToLower(spec.Auth.Header)] && !strings.EqualFold(spec.Auth.Header, "x-api-key") {
			return videoProfileInvalid("auth.header must be a safe custom header name")
		}
	default:
		return videoProfileInvalid("auth.scheme must be bearer, token or header")
	}
	for name, value := range mergeVideoProfileHeaders(spec.Headers, spec.Create.Headers) {
		if !validVideoProfileHeaderName(name) || videoProfileForbiddenHeaders[strings.ToLower(name)] || strings.ContainsAny(value, "\r\n") || len(value) > 512 {
			return videoProfileInvalid("header %q is not allowed in a template", name)
		}
	}
	refs := map[string]bool{}
	for name, path := range spec.Create.Response.Refs {
		if !videoProfileRefPattern.MatchString(name) || !validVideoProfileJSONPath(path) {
			return videoProfileInvalid("create.response.refs requires lowercase names and JSON paths")
		}
		refs[name] = true
	}
	if err := validateVideoProfilePath(spec.Create.Path, false, nil, false); err != nil {
		return videoProfileInvalid("create.path: %v", err)
	}
	// 查询参数携带任务 ID 时（如 /tasks?task_ids={task_id}），路径本身可以不含占位符。
	queryHasTask := false
	for _, value := range spec.Poll.Query {
		queryHasTask = queryHasTask || len(videoProfilePlaceholder.FindAllString(value, -1)) > 0
	}
	if err := validateVideoProfilePath(spec.Poll.Path, true, refs, !queryHasTask); err != nil {
		return videoProfileInvalid("poll.path: %v", err)
	}
	for name, value := range spec.Poll.Query {
		if name == "" || strings.ContainsAny(name, "&=?#") {
			return videoProfileInvalid("poll.query contains an invalid parameter name")
		}
		if err := validateVideoProfilePlaceholders(value, true, refs); err != nil {
			return videoProfileInvalid("poll.query.%s: %v", name, err)
		}
	}
	if len(spec.Create.Body) == 0 {
		return videoProfileInvalid("create.body requires at least one rule")
	}
	for i, rule := range spec.Create.Body {
		if err := rule.validate(); err != nil {
			return videoProfileInvalid("create.body[%d]: %v", i, err)
		}
	}
	if len(spec.Create.Response.TaskID) == 0 {
		return videoProfileInvalid("create.response.task_id requires at least one JSON path")
	}
	for _, path := range spec.Create.Response.TaskID {
		if !validVideoProfileJSONPath(path) {
			return videoProfileInvalid("create.response.task_id contains an invalid JSON path")
		}
	}
	for _, condition := range append(append([]VideoProfileCondition{}, spec.Create.Response.RejectedWhen...), spec.Poll.Response.CompletedInvalidWhen...) {
		if !validVideoProfileJSONPath(condition.Path) || (len(condition.In) == 0 && len(condition.NotIn) == 0 && condition.Exists == nil) {
			return videoProfileInvalid("response conditions require a path and one comparison")
		}
	}
	switch spec.Create.ImageRoleDefault {
	case "", "frames", "single_first", "reference":
	default:
		return videoProfileInvalid("create.image_role_default must be frames, single_first or reference")
	}
	response := spec.Poll.Response
	if len(response.Status) == 0 {
		return videoProfileInvalid("poll.response.status requires a JSON path")
	}
	if outputs := response.Outputs; outputs != nil {
		if !validVideoProfileJSONPath(outputs.Path) || outputs.Max < 1 {
			return videoProfileInvalid("poll.response.outputs requires a path and max of at least 1")
		}
		for _, path := range append(append([]string{}, outputs.URL...), outputs.Seconds...) {
			if !validVideoProfileJSONPath(path) {
				return videoProfileInvalid("poll.response.outputs contains an invalid JSON path")
			}
		}
		for field, value := range outputs.Filter {
			if !validVideoProfileJSONPath(field) || value == "" {
				return videoProfileInvalid("poll.response.outputs.filter requires JSON paths and values")
			}
		}
	}
	hasCompleted, hasFailed := false, false
	for raw, status := range response.StatusMap {
		if strings.TrimSpace(raw) == "" || !videoProfileTaskStatuses[status] {
			return videoProfileInvalid("poll.response.status_map values must be queued, processing, completed, failed, cancelled or expired")
		}
		hasCompleted = hasCompleted || status == "completed"
		hasFailed = hasFailed || status == "failed"
	}
	if !hasCompleted || !hasFailed {
		return videoProfileInvalid("poll.response.status_map must map at least one completed and one failed status")
	}
	if response.List != nil && (!validVideoProfileJSONPath(response.List.Path) || len(response.List.Match) == 0) {
		return videoProfileInvalid("poll.response.list requires path and match")
	}
	for _, group := range [][]string{response.TaskID, response.VideoURL, response.ErrorMessage, response.Seconds, response.Tokens, response.Resolution, response.Root, response.Status, response.ListMatch(), {response.Progress}} {
		for _, path := range group {
			if path != "" && !validVideoProfileJSONPath(path) {
				return videoProfileInvalid("poll.response contains an invalid JSON path")
			}
		}
	}
	if spec.Poll.IntervalSeconds < 0 || spec.Poll.IntervalSeconds > 3600 || spec.Poll.MaxProcessingMinutes < 0 || spec.Poll.MaxProcessingMinutes > 7*24*60 {
		return videoProfileInvalid("poll interval or max processing time is out of range")
	}
	switch spec.Content.Mode {
	case "url":
		if spec.Content.Path != "" || len(response.VideoURL) == 0 {
			return videoProfileInvalid("content.mode=url requires poll.response.video_url and no content.path")
		}
	case "path":
		if err := validateVideoProfilePath(spec.Content.Path, true, refs, true); err != nil {
			return videoProfileInvalid("content.path: %v", err)
		}
	default:
		return videoProfileInvalid("content.mode must be url or path")
	}
	if caps := spec.Capabilities; caps != nil {
		for _, role := range caps.ImageRoles {
			if role != VideoMediaRoleFirstFrame && role != VideoMediaRoleLastFrame && role != VideoMediaRoleReferenceImage {
				return videoProfileInvalid("capabilities.image_roles contains an unknown role")
			}
		}
		if caps.MaxImages < 0 || caps.MaxImages > videoCanonicalMaxMedia || caps.MaxVideos < 0 || caps.MaxVideos > videoCanonicalMaxMedia ||
			caps.MinImages < 0 || caps.MaxImages > 0 && caps.MinImages > caps.MaxImages {
			return videoProfileInvalid("capabilities media limits are out of range")
		}
		if caps.MinDuration < 0 || caps.MaxDuration < 0 || caps.MaxDuration > 0 && caps.MinDuration > caps.MaxDuration || caps.MaxReferenceBytes < 0 {
			return videoProfileInvalid("capabilities duration or reference size limits are out of range")
		}
		switch caps.AdaptiveRatio {
		case "", "orientation", "omit":
		default:
			return videoProfileInvalid("capabilities.adaptive_ratio must be orientation or omit")
		}
		for _, model := range caps.DenyModels {
			if strings.TrimSpace(strings.TrimSuffix(model, "*")) == "" || strings.Contains(strings.TrimSuffix(model, "*"), "*") {
				return videoProfileInvalid("capabilities.deny_models requires exact models or prefixes ending with *")
			}
		}
		for size, tier := range caps.SizeResolutions {
			if strings.TrimSpace(size) == "" || strings.TrimSpace(tier) == "" {
				return videoProfileInvalid("capabilities.size_resolutions requires non-empty sizes and tiers")
			}
		}
	}
	// 用一份覆盖全部字段的样例空跑，确保规则不会在真实请求时才暴露结构冲突。
	duration := 5.0
	sample := &VideoCanonicalRequest{Model: "sample-model", Prompt: "sample", Duration: &duration, Resolution: "720p", AspectRatio: "16:9"}
	if _, _, err := buildVideoProfileCreateBody(spec, sample, true); err != nil {
		return videoProfileInvalid("create.body cannot build a sample request: %v", err)
	}
	return nil
}

func (rule VideoProfileRule) validate() error {
	if !validVideoProfileJSONPath(rule.To) {
		return fmt.Errorf("to must be a JSON path")
	}
	sources := 0
	for _, set := range []bool{rule.From != "", len(rule.FromAll) > 0, len(rule.Const) > 0} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("exactly one of from, from_all or const is required")
	}
	switch rule.Type {
	case "", "string", "int", "number", "bool", "string_number", "string_int", "resolution", "resolution_upper", "resolution_tier", "ratio":
	default:
		return fmt.Errorf("type must be string, int, number, bool, string_number, string_int, resolution, resolution_upper, resolution_tier or ratio")
	}
	if len(rule.Default) > 0 && (!json.Valid(rule.Default) || rule.Required || len(rule.Const) > 0) {
		return fmt.Errorf("default must be a JSON value and cannot be combined with required or const")
	}
	if len(rule.Const) > 0 {
		if !json.Valid(rule.Const) || rule.Type != "" || rule.Map != nil || rule.Each != nil || rule.Item != "" || rule.Roles != nil || rule.Required {
			return fmt.Errorf("const only accepts a JSON value")
		}
		return nil
	}
	if len(rule.FromAll) > 0 {
		if len(rule.FromAll) < 2 || rule.Map == nil || rule.Each != nil || rule.Item != "" || rule.Roles != nil || rule.Append {
			return fmt.Errorf("from_all requires at least two scalar sources and a map")
		}
		for _, from := range rule.FromAll {
			if !videoProfileScalarSources[from] {
				return fmt.Errorf("from_all only accepts scalar canonical fields")
			}
		}
		return nil
	}
	_, list, err := parseVideoProfileSource(rule.From)
	if err != nil {
		return err
	}
	if list {
		if (rule.Each == nil) == (rule.Item == "") || rule.Type != "" || rule.Map != nil {
			return fmt.Errorf("media lists require exactly one of each or item")
		}
		if rule.Item != "" && rule.Item != "$url" && rule.Item != "$role" {
			return fmt.Errorf("item must be $url or $role")
		}
		for key, value := range rule.Each {
			if !validVideoProfileJSONPath(key) || value == "" || value == "$value" {
				return fmt.Errorf("each requires JSON paths and values")
			}
		}
		for role, renamed := range rule.Roles {
			if role == "" || strings.TrimSpace(renamed) == "" {
				return fmt.Errorf("roles requires non-empty names")
			}
		}
		return nil
	}
	if rule.Item != "" || rule.Roles != nil {
		return fmt.Errorf("item and roles only apply to media lists")
	}
	if rule.Append && rule.Each == nil {
		return fmt.Errorf("append on a scalar source requires an each object template")
	}
	for key, value := range rule.Each {
		if !validVideoProfileJSONPath(key) || value == "" || value == "$url" || value == "$role" {
			return fmt.Errorf("scalar each templates only accept $value and literals")
		}
	}
	return nil
}

// videoProfileSource 描述规则读取的规范字段：标量、媒体列表，或按下标/角色选中的单个媒体 URL。
type videoProfileSource struct {
	field string
	kind  string
	index int
	role  string
	extra string
}

var videoProfileMediaSelector = regexp.MustCompile(`^(images|videos)(?:\[(\d+|role=[a-z_]+)\])?$`)

// parseVideoProfileSource 返回来源及其是否为媒体列表。
func parseVideoProfileSource(from string) (videoProfileSource, bool, error) {
	if key, ok := strings.CutPrefix(from, "extra."); ok {
		if key == "" || strings.ContainsAny(key, ".[]*?#") {
			return videoProfileSource{}, false, fmt.Errorf("extra source requires a top-level key")
		}
		return videoProfileSource{kind: "extra", extra: key}, false, nil
	}
	if videoProfileScalarSources[from] {
		return videoProfileSource{field: from, kind: "scalar"}, false, nil
	}
	match := videoProfileMediaSelector.FindStringSubmatch(from)
	if match == nil {
		return videoProfileSource{}, false, fmt.Errorf("unknown source %q", from)
	}
	source := videoProfileSource{field: match[1], kind: "list", index: -1}
	if match[2] == "" {
		return source, true, nil
	}
	source.kind = "item"
	if role, ok := strings.CutPrefix(match[2], "role="); ok {
		source.role = role
	} else {
		source.index, _ = strconv.Atoi(match[2])
	}
	return source, false, nil
}

func validVideoProfileHeaderName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// validVideoProfileJSONPath 只接受点分路径与数字下标，避免 gjson/sjson 的查询语法被滥用。
func validVideoProfileJSONPath(path string) bool {
	if path == "" || len(path) > 128 {
		return false
	}
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return false
			}
		}
	}
	return true
}

// validateVideoProfilePath 只允许以 / 开头的相对路径，禁止主机、查询串、片段与目录穿越。
func validateVideoProfilePath(path string, task bool, refs map[string]bool, requireTask bool) error {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "//") || strings.ContainsAny(path, "?#\\ ") || strings.Contains(path, "..") || len(path) > 256 {
		return fmt.Errorf("must be a relative path starting with / without query, fragment or ..")
	}
	if err := validateVideoProfilePlaceholders(path, task, refs); err != nil {
		return err
	}
	if requireTask && !strings.Contains(path, "{task_id}") && len(videoProfilePlaceholder.FindAllString(path, -1)) == 0 {
		return fmt.Errorf("must reference {task_id} or a saved ref")
	}
	return nil
}

func validateVideoProfilePlaceholders(text string, task bool, refs map[string]bool) error {
	for _, match := range videoProfilePlaceholder.FindAllStringSubmatch(text, -1) {
		name := match[1]
		switch {
		case name == "model":
		case name == "task_id" && task:
		case strings.HasPrefix(name, "ref.") && task && refs[strings.TrimPrefix(name, "ref.")]:
		default:
			return fmt.Errorf("unsupported placeholder {%s}", name)
		}
	}
	if strings.Count(text, "{") != strings.Count(text, "}") {
		return fmt.Errorf("unbalanced placeholder braces")
	}
	return nil
}

// sortedVideoProfileStatuses 返回状态映射的有序键，便于稳定展示。
func sortedVideoProfileStatuses(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// videoProfileAuthHeader 按模板鉴权方式生成请求头，凭据只来自账号。
func videoProfileAuthHeader(spec VideoProtocolProfileSpec, header http.Header, token string) {
	switch spec.Auth.Scheme {
	case "token":
		header.Set("Authorization", "Token "+token)
	case "header":
		header.Del("Authorization")
		header.Set(spec.Auth.Header, token)
	default:
		header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range spec.Headers {
		header.Set(name, value)
	}
}

// ListMatch 返回列表匹配路径，便于统一校验。
func (r VideoProfilePollResponse) ListMatch() []string {
	if r.List == nil {
		return nil
	}
	return r.List.Match
}

// mergeVideoProfileHeaders 合并全局与创建请求头，仅用于统一校验。
func mergeVideoProfileHeaders(groups ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, group := range groups {
		for name, value := range group {
			out[name] = value
		}
	}
	return out
}
