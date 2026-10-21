package service

import (
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
	"github.com/tidwall/gjson"
)

// 夜总会协议使用独立分辨率和比例，不借用 OpenAI 官方像素尺寸及模型默认值。
// @project-doc docs/interfaces/video_upstream.md#video_adapters
func prepareVideoYezonghui(values map[string]json.RawMessage) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	allowed := map[string]bool{
		"model": true, "prompt": true, "seconds": true, "duration": true,
		"resolution": true, "size": true, "aspect_ratio": true, "ratio": true,
		"image_urls": true, "images": true, "video_urls": true, "audio_urls": true,
		"negative_prompt": true, "n": true, "seed": true, "fps": true,
		"camera_fixed": true, "audio": true, "generate_audio": true, "metadata": true,
	}
	for key := range values {
		if !allowed[key] {
			return nil, metadata, videoProtocolError("Unsupported Yezonghui parameter; use metadata for vendor extensions: " + key)
		}
	}
	root := videoProtocolRoot(values)
	if prompt := root.Get("prompt"); prompt.Type != gjson.String || strings.TrimSpace(prompt.String()) == "" {
		return nil, metadata, videoProtocolError("Yezonghui prompt is required")
	}
	seconds, present, err := videoYezonghuiSeconds(root)
	if err != nil || !present {
		return nil, metadata, videoProtocolError("Yezonghui requires integer seconds from 1 to 3600")
	}
	videoProtocolPut(values, "seconds", strconv.FormatInt(int64(seconds), 10))
	delete(values, "duration")
	metadata.DurationSeconds = seconds

	// 厂商扩展保留原始数字精度；显式的嵌套档位必须与顶层一致，不能绕过报价。
	vendor, err := videoProtocolObject(values, "metadata")
	if err != nil {
		return nil, metadata, err
	}
	resolution := ""
	for _, scope := range []map[string]json.RawMessage{values, vendor} {
		if raw, ok := scope["resolution"]; ok {
			current, parseErr := videoYezonghuiResolution(gjson.ParseBytes(raw))
			if parseErr != nil {
				return nil, metadata, parseErr
			}
			if resolution != "" && resolution != current {
				return nil, metadata, videoProtocolError("Conflicting Yezonghui resolution fields")
			}
			resolution = current
		}
	}
	if resolution == "" {
		return nil, metadata, videoProtocolError("Yezonghui requires an explicit resolution")
	}
	videoProtocolPut(values, "resolution", resolution)
	metadata.Resolution = resolution
	if _, exists := vendor["resolution"]; exists {
		videoProtocolPut(vendor, "resolution", resolution)
		videoProtocolPut(values, "metadata", vendor)
	}
	// 画布旧比例与文档别名仅在完全一致时合并；自动比例没有已声明的供应商语义。
	ratio := ""
	for _, field := range []string{"size", "ratio", "aspect_ratio"} {
		if value := root.Get(field); value.Exists() {
			if !validVideoYezonghuiRatio(value) {
				return nil, metadata, videoProtocolError("Yezonghui size must be a positive WIDTH:HEIGHT ratio")
			}
			if ratio != "" && ratio != value.String() {
				return nil, metadata, videoProtocolError("Conflicting Yezonghui aspect ratios")
			}
			ratio = value.String()
		}
	}
	delete(values, "ratio")
	delete(values, "aspect_ratio")
	if ratio != "" {
		videoProtocolPut(values, "size", ratio)
	}
	for _, pair := range [][2]string{{"images", "image_urls"}, {"generate_audio", "audio"}} {
		if err := videoProtocolMove(values, values, pair[0], pair[1]); err != nil {
			return nil, metadata, err
		}
	}
	root = videoProtocolRoot(values)
	imageCount := 0
	for _, field := range []string{"image_urls", "video_urls", "audio_urls"} {
		value := root.Get(field)
		if !value.Exists() {
			continue
		}
		if !value.IsArray() {
			return nil, metadata, videoProtocolError("Yezonghui " + field + " must be a URL array")
		}
		for _, item := range value.Array() {
			if item.Type != gjson.String || !validVideoYezonghuiURL(item.String()) {
				return nil, metadata, videoProtocolError("Yezonghui media requires public HTTP(S) URLs")
			}
		}
		if field == "image_urls" {
			imageCount = len(value.Array())
		}
		if field == "video_urls" {
			metadata.HasReferenceVideo = len(value.Array()) > 0
		}
	}
	metadata.ReferenceImageCount = &imageCount
	for _, field := range []string{"audio", "camera_fixed"} {
		if value := root.Get(field); value.Exists() && value.Type != gjson.True && value.Type != gjson.False {
			return nil, metadata, videoProtocolError("Yezonghui " + field + " must be a boolean")
		}
	}
	if value := root.Get("negative_prompt"); value.Exists() && value.Type != gjson.String {
		return nil, metadata, videoProtocolError("Yezonghui negative_prompt must be a string")
	}
	for _, field := range []string{"n", "seed", "fps"} {
		value := root.Get(field)
		if !value.Exists() {
			continue
		}
		n, parseErr := strconv.ParseInt(value.Raw, 10, 64)
		if value.Type != gjson.Number || parseErr != nil || field == "n" && n != 1 || field == "fps" && n < 1 {
			return nil, metadata, videoProtocolError("Invalid Yezonghui integer parameter: " + field)
		}
	}
	body, err := json.Marshal(values)
	return body, metadata, err
}

// 档位沿用本站可报价的数字 p、2K/4K 集合，实际模型允许值仍由供应商检查。
func videoYezonghuiResolution(value gjson.Result) (string, error) {
	if value.Type != gjson.String {
		return "", videoProtocolError("Yezonghui resolution must be a string tier")
	}
	resolution, err := NormalizeVideoPriceResolution(value.String())
	if err != nil || strings.Contains(resolution, "x") {
		return "", videoProtocolError("Yezonghui resolution must be an explicit tier, not pixel dimensions or a ratio")
	}
	return resolution, nil
}

// 字符串只允许 seconds，JSON 小数及指数记法不会被浮点转换成看似合法的整数。
func videoYezonghuiSeconds(root gjson.Result) (float64, bool, error) {
	var seconds int64
	present := false
	for _, field := range []string{"seconds", "duration"} {
		value := root.Get(field)
		if !value.Exists() {
			continue
		}
		raw := value.Raw
		if value.Type == gjson.String && field == "seconds" {
			raw = value.String()
		} else if value.Type != gjson.Number {
			return 0, false, videoProtocolError("Yezonghui seconds must be an integer or integer string")
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 || n > 3600 || strconv.FormatInt(n, 10) != raw || present && n != seconds {
			return 0, false, videoProtocolError("Invalid or conflicting Yezonghui seconds")
		}
		seconds, present = n, true
	}
	return float64(seconds), present, nil
}

// 不按示例枚举锁死比例，但限制为无歧义、大小有界的正整数对。
func validVideoYezonghuiRatio(value gjson.Result) bool {
	if value.Type != gjson.String {
		return false
	}
	parts := strings.Split(value.String(), ":")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > 65536 || strconv.Itoa(n) != part {
			return false
		}
	}
	return true
}

// 这里只校验 URL 形态和显式内网主机，不访问素材；上传及实际下载另有 DNS/重定向检查。
func validVideoYezonghuiURL(raw string) bool {
	if !safeMediaPreviewURL(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || strings.Contains(u.Hostname(), "%") {
		return false
	}
	// 与临时参考素材入口保持一致：DNS 绝对名称的末尾点不能绕过内网字面量检查。
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return !strings.HasSuffix(host, ".") && !urlvalidator.IsBlockedHost(host) &&
		!strings.HasSuffix(host, ".local") && !strings.HasSuffix(host, ".internal") &&
		(strings.Contains(host, ".") || net.ParseIP(host) != nil)
}

// 新路径只承诺顶层任务 ID；厂商 metadata 中的同名扩展不参与归属判断。
func videoYezonghuiResponseIdentityMatches(body []byte, expected string) bool {
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() {
		return !root.Get("id").Exists() && !root.Get("task_id").Exists()
	}
	seen := map[string]bool{}
	valid := true
	root.ForEach(func(key, value gjson.Result) bool {
		name := strings.ToLower(key.String())
		if name != "id" && name != "task_id" {
			return true
		}
		if key.String() != name || seen[name] || value.Type != gjson.String || value.String() == "" || strings.TrimSpace(value.String()) != value.String() || validateUpstreamPathSegment("video task ID", value.String()) != nil {
			valid = false
			return false
		}
		seen[name] = true
		if expected != "" && expected != value.String() {
			valid = false
			return false
		}
		expected = value.String()
		return true
	})
	return valid
}

// 独立解析避免把比例 size 送入像素价档检查；缺少下载地址由原 /content 链路处理。
func extractVideoYezonghuiResponse(status int, body []byte, expected string) *VideoUpstreamResponse {
	out := &VideoUpstreamResponse{StatusCode: status, Body: body, TaskID: expected}
	if !videoYezonghuiResponseIdentityMatches(body, expected) {
		out.TaskID = ""
		out.MetadataError = "Conflicting Yezonghui video task identifiers"
		return out
	}
	root := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !root.IsObject() {
		return out
	}
	if out.TaskID == "" {
		out.TaskID = firstVideoString(root, "id", "task_id")
	}
	if err := validateVideoJSONKeys(body); err != nil || !videoOpenAIKeysCanonical(root, "id", "task_id", "status", "seconds", "url") {
		out.MetadataError = "Ambiguous Yezonghui video response"
		return out
	}
	if object := root.Get("object"); object.Exists() && (object.Type != gjson.String || object.String() != "video") {
		out.MetadataError = "Unexpected Yezonghui response object"
		return out
	}
	out.UpstreamStatus = firstVideoString(root, "status")
	switch out.UpstreamStatus {
	case "queued":
		out.Status = "queued"
	case "in_progress":
		out.Status = "processing"
	case "completed", "failed":
		out.Status = out.UpstreamStatus
	}
	// unknown、未来状态和旧接口大写状态不属于终态，不能据此退款或交付产物。
	if out.Status != "completed" {
		return out
	}
	// 新查询成功包没有业务错误或旧接口外壳；冲突包不能凭 completed 沿用预估价结算。
	if !videoOpenAIKeysCanonical(root, "code", "error", "data", "output", "task") {
		out.MetadataError = "Ambiguous Yezonghui result envelope"
		return out
	}
	for _, field := range []string{"code", "error", "data", "output", "task"} {
		if value := root.Get(field); value.Exists() && value.Type != gjson.Null {
			out.MetadataError = "Yezonghui completed status conflicts with an error or legacy result envelope"
			return out
		}
	}
	// 出现大小写混用的计费字段时不能忽略它并沿用请求预估值。
	if !videoOpenAIKeysCanonical(root, "model", "object", "duration", "resolution", "size", "usage") ||
		(root.Get("usage").IsObject() && !videoOpenAIKeysCanonical(root.Get("usage"), "output_tokens", "completion_tokens")) {
		out.MetadataError = "Ambiguous Yezonghui video output metadata"
		return out
	}
	if seconds, present, err := videoYezonghuiSeconds(root); err != nil {
		out.MetadataError = "Invalid Yezonghui video output seconds"
		return out
	} else if present {
		out.Metadata.DurationSeconds = seconds
	}
	if value := root.Get("resolution"); value.Exists() {
		resolution, err := videoYezonghuiResolution(value)
		if err != nil {
			out.MetadataError = "Invalid Yezonghui video output resolution"
			return out
		}
		out.Metadata.Resolution = resolution
	}
	if value := root.Get("size"); value.Exists() && !validVideoYezonghuiRatio(value) {
		out.MetadataError = "Invalid Yezonghui video output aspect ratio"
		return out
	}
	if value := root.Get("url"); value.Exists() && value.Type != gjson.Null && !(value.Type == gjson.String && value.String() == "") {
		if value.Type != gjson.String || !validVideoYezonghuiURL(value.String()) {
			out.MetadataError = "Invalid Yezonghui video result URL"
			return out
		}
		out.VideoURL = value.String()
	}
	out.Metadata.Model = firstVideoString(root, "model")
	validateVideoOfficialUsage(out, root, VideoProtocolAdapterYezonghui)
	if out.MetadataError != "" {
		return out
	}
	// 文档没有承诺 Token 用量；仅明确的正数才可结算 Token 价，零占位仍待核对。
	for _, path := range []string{"usage.output_tokens", "usage.completion_tokens"} {
		if value := root.Get(path); value.Type == gjson.Number && value.Int() > 0 {
			n := value.Int()
			out.Metadata.Tokens = &n
			break
		}
	}
	return out
}
