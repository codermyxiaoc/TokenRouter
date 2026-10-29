package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// videoProfileConsumption 记录模板实际读取的规范字段；非空但未被读取的字段必须拒绝，防止静默丢参与少计费。
type videoProfileConsumption struct {
	fields map[string]bool
	images map[int]bool
	videos map[int]bool
	// defaults 记录由 default 写入的路径，之后的显式值可以覆盖它们。
	defaults map[string]bool
	// lookups 记录联合查表写出的字符串值，用于把像素尺寸换算为计价档位。
	lookups []string
}

// buildVideoProfileCreateBody 按模板生成创建请求体，并从已发送的规范值提取计价维度。
// dryRun 仅用于保存模板时的样例校验，跳过必填与未消费字段检查。
func buildVideoProfileCreateBody(spec VideoProtocolProfileSpec, req *VideoCanonicalRequest, dryRun bool) ([]byte, VideoRequestMetadata, error) {
	var metadata VideoRequestMetadata
	if req == nil {
		return nil, metadata, videoCanonicalError("VIDEO_INVALID_REQUEST", "Video request is required")
	}
	effective := *req
	if effective.Duration == nil && spec.Create.Defaults.Duration != nil {
		duration := *spec.Create.Defaults.Duration
		effective.Duration = &duration
	}
	if effective.Resolution == "" {
		effective.Resolution = spec.Create.Defaults.Resolution
	}
	if effective.AspectRatio == "" {
		effective.AspectRatio = spec.Create.Defaults.AspectRatio
	}
	effective.Images = videoProfileDefaultImageRoles(spec.Create.ImageRoleDefault, req.Images)
	if !dryRun {
		if err := validateVideoProfileCapabilities(spec.Capabilities, &effective); err != nil {
			return nil, metadata, err
		}
	}
	used := videoProfileConsumption{fields: map[string]bool{}, images: map[int]bool{}, videos: map[int]bool{}, defaults: map[string]bool{}}
	// 路径中的 {model} 同样算作已发送模型。
	if strings.Contains(spec.Create.Path, "{model}") {
		used.fields["model"] = true
	}
	body := []byte("{}")
	var err error
	for i, rule := range spec.Create.Body {
		body, err = applyVideoProfileRule(body, rule, &effective, &used, dryRun)
		if err != nil {
			return nil, metadata, fmt.Errorf("rule %d: %w", i, err)
		}
	}
	if !dryRun {
		if err := effective.ensureConsumed(&used); err != nil {
			return nil, metadata, err
		}
	}
	metadata.Model = effective.Model
	if used.fields["duration"] && effective.Duration != nil {
		metadata.DurationSeconds = *effective.Duration
	}
	var sizes map[string]string
	if spec.Capabilities != nil {
		sizes = spec.Capabilities.SizeResolutions
	}
	if used.fields["resolution"] {
		metadata.Resolution = effective.Resolution
	} else if used.fields["size"] {
		metadata.Resolution, metadata.Size = effective.Size, effective.Size
		if tier := sizes[effective.Size]; tier != "" {
			metadata.Resolution = tier
		}
	}
	for _, value := range used.lookups {
		// 联合查表写出的像素尺寸同样换算为计价档位，保留尺寸供按尺寸计价。
		if tier := sizes[value]; tier != "" {
			metadata.Resolution, metadata.Size = tier, value
		}
	}
	count := len(used.images)
	metadata.ReferenceImageCount = &count
	metadata.HasReferenceVideo = len(used.videos) > 0
	return body, metadata, nil
}

// videoProfileDefaultImageRoles 为未写角色的参考图补角色，不改动调用方的原始切片。
func videoProfileDefaultImageRoles(mode string, images []VideoCanonicalMedia) []VideoCanonicalMedia {
	if mode == "" || len(images) == 0 {
		return images
	}
	out := append([]VideoCanonicalMedia(nil), images...)
	for i := range out {
		if out[i].Role != "" {
			continue
		}
		role := VideoMediaRoleReferenceImage
		switch mode {
		case "frames":
			// 一张为首帧，两张为首尾帧，三张以上视为参考图。
			if len(out) < 3 {
				role = VideoMediaRoleFirstFrame
				if i == 1 {
					role = VideoMediaRoleLastFrame
				}
			}
		case "single_first":
			if len(out) == 1 {
				role = VideoMediaRoleFirstFrame
			}
		}
		out[i].Role = role
	}
	return out
}

// ensureConsumed 拒绝模板未映射的非空字段，包括每一张参考图和参考视频。
func (req *VideoCanonicalRequest) ensureConsumed(used *videoProfileConsumption) error {
	present := map[string]bool{
		"model": req.Model != "", "prompt": req.Prompt != "", "negative_prompt": req.NegativePrompt != "", "duration": req.Duration != nil,
		"resolution": req.Resolution != "", "aspect_ratio": req.AspectRatio != "", "size": req.Size != "",
		"generate_audio": req.GenerateAudio != nil, "watermark": req.Watermark != nil, "seed": req.Seed != nil,
	}
	for field, ok := range present {
		if ok && !used.fields[field] {
			return videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol does not support "+field)
		}
	}
	for key := range req.Extra {
		if !used.fields["extra."+key] {
			return videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol does not accept extra."+key)
		}
	}
	for i := range req.Images {
		if !used.images[i] {
			return videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol cannot send every reference image")
		}
	}
	for i := range req.Videos {
		if !used.videos[i] {
			return videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol cannot send every reference video")
		}
	}
	return nil
}

// validateVideoProfileCapabilities 按模板声明的能力范围检查请求，未声明的维度不限制。
func validateVideoProfileCapabilities(caps *VideoProfileCapabilities, req *VideoCanonicalRequest) error {
	if caps == nil {
		return nil
	}
	reject := func(field string) error {
		return videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol does not support this "+field)
	}
	for _, denied := range caps.DenyModels {
		prefix, isPrefix := strings.CutSuffix(denied, "*")
		if strings.EqualFold(req.Model, denied) || isPrefix && strings.HasPrefix(strings.ToLower(req.Model), strings.ToLower(prefix)) {
			return reject("model")
		}
	}
	if req.Duration != nil {
		d := *req.Duration
		if len(caps.Durations) > 0 && !containsFloat(caps.Durations, d) || caps.IntegerDuration && d != math.Trunc(d) ||
			caps.MinDuration > 0 && d < caps.MinDuration || caps.MaxDuration > 0 && d > caps.MaxDuration {
			return reject("duration")
		}
	}
	if len(caps.Resolutions) > 0 && req.Resolution != "" && !containsFold(caps.Resolutions, req.Resolution) {
		return reject("resolution")
	}
	if len(caps.AspectRatios) > 0 && req.AspectRatio != "" && !containsFold(caps.AspectRatios, req.AspectRatio) {
		return reject("aspect_ratio")
	}
	if caps.MaxImages > 0 && len(req.Images) > caps.MaxImages {
		return reject("number of reference images")
	}
	if len(req.Images) < caps.MinImages {
		return videoCanonicalError("VIDEO_PARAMETER_REQUIRED", fmt.Sprintf("The configured video protocol requires at least %d reference images", caps.MinImages))
	}
	if len(caps.ImageRoles) > 0 {
		for _, image := range req.Images {
			if image.Role != "" && !containsFold(caps.ImageRoles, image.Role) {
				return reject("image role")
			}
		}
	}
	if caps.MaxVideos > 0 && len(req.Videos) > caps.MaxVideos {
		return reject("number of reference videos")
	}
	if caps.ExclusiveFrames {
		first, last, references := 0, 0, len(req.Videos)
		for _, image := range req.Images {
			switch image.Role {
			case VideoMediaRoleFirstFrame:
				first++
			case VideoMediaRoleLastFrame:
				last++
			default:
				references++
			}
		}
		if first > 1 || last > 1 || last > 0 && first == 0 || first+last > 0 && references > 0 {
			return videoCanonicalError("VIDEO_PARAMETER_CONFLICT", "Frame inputs cannot be mixed with reference inputs")
		}
	}
	if caps.RequireRatioWithoutMedia && len(req.Images)+len(req.Videos) == 0 && (req.AspectRatio == "" || req.AspectRatio == "adaptive") {
		return videoCanonicalError("VIDEO_PARAMETER_REQUIRED", "Text-to-video requires a non-adaptive aspect_ratio")
	}
	if caps.PublicMedia {
		for _, item := range append(append([]VideoCanonicalMedia{}, req.Images...), req.Videos...) {
			if !validVideoYezonghuiURL(item.URL) {
				return videoCanonicalError("VIDEO_PARAMETER_INVALID", "The configured video protocol requires public HTTP(S) media URLs")
			}
		}
	}
	return nil
}

func containsFloat(values []float64, target float64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

// applyVideoProfileRule 执行一条映射规则；来源缺失时写入默认值或跳过，标记必填时报错。
func applyVideoProfileRule(body []byte, rule VideoProfileRule, req *VideoCanonicalRequest, used *videoProfileConsumption, dryRun bool) ([]byte, error) {
	if len(rule.Const) > 0 {
		return videoProfileSet(body, rule.To, json.RawMessage(rule.Const), false, used)
	}
	missing := func(name string) ([]byte, error) {
		if rule.Required && !dryRun {
			return nil, videoCanonicalError("VIDEO_PARAMETER_REQUIRED", name+" is required by the configured video protocol")
		}
		if len(rule.Default) > 0 {
			if existing := gjson.GetBytes(body, rule.To); existing.Exists() {
				return body, nil
			}
			used.defaults[rule.To] = true
			return sjson.SetRawBytes(body, rule.To, rule.Default)
		}
		return body, nil
	}
	if len(rule.FromAll) > 0 {
		return applyVideoProfileLookup(body, rule, req, used, dryRun, missing)
	}
	source, list, err := parseVideoProfileSource(rule.From)
	if err != nil {
		return nil, err
	}
	if list {
		return applyVideoProfileMediaList(body, rule, source, req, used, dryRun, missing)
	}
	value, present, err := videoProfileSourceValue(source, req, used)
	if err != nil {
		return nil, err
	}
	if !present {
		return missing(rule.From)
	}
	if rule.Map != nil {
		mapped, exists := rule.Map[videoProfileMapKey(value)]
		if !exists {
			if dryRun {
				return body, nil
			}
			return nil, videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol does not support "+rule.From+"="+videoProfileMapKey(value))
		}
		value = mapped
	}
	if value, err = convertVideoProfileValue(value, rule.Type); err != nil {
		return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", rule.From+": "+err.Error())
	}
	if rule.Each != nil {
		// 标量包装成对象，例如把提示词追加为 contents 数组中的文本项。
		rendered := []byte("{}")
		for path, template := range rule.Each {
			var item any = template
			if template == "$value" {
				item = value
			}
			if rendered, err = sjson.SetBytes(rendered, path, item); err != nil {
				return nil, err
			}
		}
		value = json.RawMessage(rendered)
	}
	return videoProfileSet(body, rule.To, value, rule.Append, used)
}

// applyVideoProfileLookup 联合多个字段查表，例如由分辨率档位与比例得到像素尺寸。
func applyVideoProfileLookup(body []byte, rule VideoProfileRule, req *VideoCanonicalRequest, used *videoProfileConsumption, dryRun bool, missing func(string) ([]byte, error)) ([]byte, error) {
	keys := make([]string, 0, len(rule.FromAll))
	present := 0
	for _, from := range rule.FromAll {
		value, ok, err := videoProfileSourceValue(videoProfileSource{field: from, kind: "scalar"}, req, used)
		if err != nil {
			return nil, err
		}
		if ok {
			present++
		}
		keys = append(keys, videoProfileMapKey(value))
	}
	names := strings.Join(rule.FromAll, ", ")
	if present == 0 {
		return missing(names)
	}
	if present != len(rule.FromAll) {
		if dryRun {
			return body, nil
		}
		return nil, videoCanonicalError("VIDEO_PARAMETER_REQUIRED", "The configured video protocol requires all of "+names)
	}
	key := strings.Join(keys, "|")
	mapped, exists := rule.Map[key]
	if !exists {
		if dryRun {
			return body, nil
		}
		return nil, videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol does not support "+names+"="+key)
	}
	value, err := convertVideoProfileValue(mapped, rule.Type)
	if err != nil {
		return nil, videoCanonicalError("VIDEO_PARAMETER_INVALID", names+": "+err.Error())
	}
	if text, ok := value.(string); ok {
		used.lookups = append(used.lookups, text)
	}
	return videoProfileSet(body, rule.To, value, false, used)
}

// applyVideoProfileMediaList 渲染媒体列表：对象数组（each）或字符串数组（item），可追加到已有数组。
func applyVideoProfileMediaList(body []byte, rule VideoProfileRule, source videoProfileSource, req *VideoCanonicalRequest, used *videoProfileConsumption, dryRun bool, missing func(string) ([]byte, error)) ([]byte, error) {
	media, mark := req.Images, used.images
	if source.field == "videos" {
		media, mark = req.Videos, used.videos
	}
	if len(media) == 0 {
		return missing(source.field)
	}
	items := make([]any, 0, len(media))
	for i, item := range media {
		role := item.Role
		if rule.Roles != nil && role != "" {
			renamed, ok := rule.Roles[role]
			if !ok {
				if dryRun {
					continue
				}
				return nil, videoCanonicalError("VIDEO_PARAMETER_UNSUPPORTED", "The configured video protocol does not support "+source.field+" role "+role)
			}
			role = renamed
		}
		render := func(template string) string {
			switch template {
			case "$url":
				return item.URL
			case "$role":
				return role
			}
			return template
		}
		if rule.Item != "" {
			value := render(rule.Item)
			if value == "" {
				return nil, videoCanonicalError("VIDEO_PARAMETER_REQUIRED", "The configured video protocol requires a role for every "+source.field+" item")
			}
			items = append(items, value)
		} else {
			rendered := []byte("{}")
			var err error
			for path, template := range rule.Each {
				// 空值（例如未写角色）直接省略，不发送空字符串。
				if value := render(template); value != "" {
					if rendered, err = sjson.SetBytes(rendered, path, value); err != nil {
						return nil, err
					}
				}
			}
			items = append(items, json.RawMessage(rendered))
		}
		mark[i] = true
	}
	if !rule.Append {
		return videoProfileSet(body, rule.To, items, false, used)
	}
	var err error
	for _, item := range items {
		if body, err = videoProfileSet(body, rule.To, item, true, used); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// videoProfileSet 写入或追加一个值；两条规则向同一路径写入不同值时拒绝，不能静默覆盖已发送参数。
func videoProfileSet(body []byte, path string, value any, appendItem bool, used *videoProfileConsumption) ([]byte, error) {
	existing := gjson.GetBytes(body, path)
	if appendItem {
		if existing.Exists() && !existing.IsArray() {
			return nil, videoCanonicalError("VIDEO_PARAMETER_CONFLICT", "The configured video protocol appends to a non-array field "+path)
		}
		return sjson.SetBytes(body, path+".-1", value)
	}
	if existing.Exists() && !used.defaults[path] {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var left, right bytes.Buffer
		if json.Compact(&left, encoded) != nil || json.Compact(&right, []byte(existing.Raw)) != nil || left.String() != right.String() {
			return nil, videoCanonicalError("VIDEO_PARAMETER_CONFLICT", "Conflicting video parameters for "+path)
		}
		return body, nil
	}
	delete(used.defaults, path)
	return sjson.SetBytes(body, path, value)
}

// videoProfileMapKey 把取值转为映射表键：布尔为 true/false，数字为最短十进制表示。
func videoProfileMapKey(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.RawMessage:
		var text string
		if json.Unmarshal(v, &text) == nil {
			return text
		}
		return string(bytes.TrimSpace(v))
	case nil:
		return ""
	}
	return fmt.Sprint(value)
}

// videoProfileSourceValue 读取规范字段的值并标记为已消费。
func videoProfileSourceValue(source videoProfileSource, req *VideoCanonicalRequest, used *videoProfileConsumption) (any, bool, error) {
	switch source.kind {
	case "extra":
		raw, ok := req.Extra[source.extra]
		if !ok {
			return nil, false, nil
		}
		used.fields["extra."+source.extra] = true
		return json.RawMessage(raw), true, nil
	case "item":
		media, mark := req.Images, used.images
		if source.field == "videos" {
			media, mark = req.Videos, used.videos
		}
		selected := -1
		for i, item := range media {
			if source.role == "" && i == source.index || source.role != "" && item.Role == source.role {
				if selected >= 0 {
					return nil, false, videoCanonicalError("VIDEO_PARAMETER_CONFLICT", "More than one "+source.field+" item has role "+source.role)
				}
				selected = i
			}
		}
		if selected < 0 {
			return nil, false, nil
		}
		mark[selected] = true
		return media[selected].URL, true, nil
	}
	var value any
	present := true
	switch source.field {
	case "model":
		value, present = req.Model, req.Model != ""
	case "prompt":
		value, present = req.Prompt, req.Prompt != ""
	case "negative_prompt":
		value, present = req.NegativePrompt, req.NegativePrompt != ""
	case "resolution":
		value, present = req.Resolution, req.Resolution != ""
	case "aspect_ratio":
		value, present = req.AspectRatio, req.AspectRatio != ""
	case "size":
		value, present = req.Size, req.Size != ""
	case "duration":
		present = req.Duration != nil
		if present {
			value = *req.Duration
		}
	case "generate_audio":
		present = req.GenerateAudio != nil
		if present {
			value = *req.GenerateAudio
		}
	case "watermark":
		present = req.Watermark != nil
		if present {
			value = *req.Watermark
		}
	case "seed":
		present = req.Seed != nil
		if present {
			value = *req.Seed
		}
	}
	if present {
		used.fields[source.field] = true
	}
	return value, present, nil
}

// convertVideoProfileValue 只做无损类型转换；整数要求原值没有小数部分。
func convertVideoProfileValue(value any, kind string) (any, error) {
	// rawText 保留 JSON 原文，整数可直接从原文精确解析，不经 float64 舍入。
	rawText := ""
	if raw, ok := value.(json.RawMessage); ok {
		if kind == "" {
			return raw, nil
		}
		rawText = strings.TrimSpace(string(raw))
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, err
		}
		value = decoded
	}
	number := func() (float64, error) {
		switch v := value.(type) {
		case float64:
			return v, nil
		case int64:
			return float64(v), nil
		case int:
			return float64(v), nil
		case string:
			return strconv.ParseFloat(strings.TrimSpace(v), 64)
		}
		return 0, fmt.Errorf("value is not numeric")
	}
	// exactInt 先按整数精确解析，避免大整数经 float64 舍入后被当作合法值静默改写。
	// 指数或带小数点的写法（9007199254740993e0、...993.0）也按十进制精确求值；
	// 能精确表示为整数的返回 ok，带非零小数或超出 int64 的返回 err，绝不经 float64 舍入。
	exactInt := func() (int64, bool, error) {
		text := ""
		switch v := value.(type) {
		case int64:
			return v, true, nil
		case int:
			return int64(v), true, nil
		case float64:
			text = rawText
		case string:
			text = strings.TrimSpace(v)
		}
		if text == "" {
			return 0, false, nil
		}
		exact, ok := new(big.Rat).SetString(text)
		if !ok {
			return 0, false, nil
		}
		if !exact.IsInt() || !exact.Num().IsInt64() {
			return 0, false, fmt.Errorf("value must be an integer")
		}
		return exact.Num().Int64(), true, nil
	}
	switch kind {
	case "":
		return value, nil
	case "string":
		switch v := value.(type) {
		case string:
			return v, nil
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64), nil
		case int64:
			return strconv.FormatInt(v, 10), nil
		case bool:
			return strconv.FormatBool(v), nil
		}
		return nil, fmt.Errorf("value cannot be converted to string")
	case "int":
		if exact, ok, exactErr := exactInt(); exactErr != nil {
			return nil, exactErr
		} else if ok {
			if exact > 1<<53 || exact < -(1<<53) {
				return nil, fmt.Errorf("value must be an integer")
			}
			return exact, nil
		}
		n, err := number()
		// 超出安全整数范围或非有限数会在 int64 转换时静默溢出，必须在发往上游前明确拒绝。
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return nil, fmt.Errorf("value must be an integer")
		}
		return int64(n), nil
	case "number":
		n, err := number()
		if err != nil {
			return nil, err
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("value must be a finite number")
		}
		return n, nil
	case "string_number":
		n, err := number()
		if err != nil {
			return nil, err
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("value must be a finite number")
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case "string_int":
		if exact, ok, exactErr := exactInt(); exactErr != nil {
			return nil, exactErr
		} else if ok {
			if exact > 1<<53 || exact < -(1<<53) {
				return nil, fmt.Errorf("value must be an integer")
			}
			return strconv.FormatInt(exact, 10), nil
		}
		n, err := number()
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return nil, fmt.Errorf("value must be an integer")
		}
		return strconv.FormatInt(int64(n), 10), nil
	case "resolution", "resolution_upper":
		// 校验分辨率可计价后统一大小写：多数上游用小写，MiniMax 与万相用大写。
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("value must be a resolution string")
		}
		text = strings.TrimSpace(text)
		if _, err := NormalizeVideoPriceResolution(text); err != nil {
			return nil, fmt.Errorf("value is not a valid resolution")
		}
		if kind == "resolution_upper" {
			return strings.ToUpper(text), nil
		}
		return strings.ToLower(text), nil
	case "resolution_tier":
		// 只接受分辨率档位（如 720p、1080p、2k），像素尺寸或比例一律拒绝。
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("value must be a resolution tier")
		}
		tier, err := NormalizeVideoPriceResolution(text)
		if err != nil || strings.Contains(tier, "x") {
			return nil, fmt.Errorf("value must be a resolution tier, not pixel dimensions or a ratio")
		}
		return tier, nil
	case "ratio":
		text, ok := value.(string)
		if !ok || !validVideoProfileRatio(text) {
			return nil, fmt.Errorf("value must be a positive WIDTH:HEIGHT ratio")
		}
		return text, nil
	case "bool":
		if v, ok := value.(bool); ok {
			return v, nil
		}
		return nil, fmt.Errorf("value must be a boolean")
	}
	return nil, fmt.Errorf("unsupported type %q", kind)
}

// validVideoProfileRatio 校验 W:H 正整数比例。
func validVideoProfileRatio(text string) bool {
	parts := strings.Split(text, ":")
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

// videoProfileSpecFromTarget 读取任务冻结的模板快照并校验哈希，被篡改或缺失的快照一律拒绝。
func videoProfileSpecFromTarget(target VideoUpstreamTarget) (VideoProtocolProfileSpec, error) {
	var spec VideoProtocolProfileSpec
	if target.Version != videoTargetVersionProfile || len(target.ProfileSnapshot) == 0 {
		return spec, videoProtocolError("Video profile snapshot is missing")
	}
	if err := json.Unmarshal(target.ProfileSnapshot, &spec); err != nil {
		return spec, videoProtocolError("Video profile snapshot is invalid")
	}
	hash, err := spec.Hash()
	if err != nil || hash != target.ProfileHash {
		return spec, videoProtocolError("Video profile snapshot hash mismatch")
	}
	return spec, nil
}

// renderVideoProfilePath 把 {task_id}、{model}、{ref.x} 替换为转义后的值；缺失的引用视为不可查询。
func renderVideoProfilePath(path string, target VideoUpstreamTarget, taskID string, query bool) (string, error) {
	var renderErr error
	escape := url.PathEscape
	if query {
		escape = url.QueryEscape
	}
	rendered := videoProfilePlaceholder.ReplaceAllStringFunc(path, func(match string) string {
		name := strings.Trim(match, "{}")
		var value string
		switch {
		case name == "task_id":
			value = taskID
		case name == "model":
			value = target.Model
		case strings.HasPrefix(name, "ref."):
			value = target.TaskRefs[strings.TrimPrefix(name, "ref.")]
		}
		if strings.TrimSpace(value) == "" || validateUpstreamPathSegment("video path value", value) != nil {
			renderErr = fmt.Errorf("video path placeholder %s is unavailable", match)
			return ""
		}
		return escape(value)
	})
	return rendered, renderErr
}

// videoProfileTaskPath 生成冻结模板的查询路径（含查询参数）。
func videoProfileTaskPath(target VideoUpstreamTarget, taskID string) (string, error) {
	spec, err := videoProfileSpecFromTarget(target)
	if err != nil {
		return "", err
	}
	path, err := renderVideoProfilePath(spec.Poll.Path, target, taskID, false)
	if err != nil {
		return "", err
	}
	if len(spec.Poll.Query) > 0 {
		values := url.Values{}
		for _, name := range sortedVideoProfileStatuses(spec.Poll.Query) {
			value, err := renderVideoProfilePath(spec.Poll.Query[name], target, taskID, true)
			if err != nil {
				return "", err
			}
			unescaped, _ := url.QueryUnescape(value)
			values.Set(name, unescaped)
		}
		path += "?" + values.Encode()
	}
	return path, nil
}

// videoProfileContentPath 返回模板声明的内容路径；直链模式返回空字符串。
func videoProfileContentPath(target VideoUpstreamTarget, taskID string) (string, error) {
	spec, err := videoProfileSpecFromTarget(target)
	if err != nil {
		return "", err
	}
	if spec.Content.Mode != "path" {
		return "", nil
	}
	return renderVideoProfilePath(spec.Content.Path, target, taskID, false)
}

// videoProfileIDs 读取声明路径上的任务 ID；值不一致时 conflict 为真，不能任选其一。
func videoProfileIDs(root gjson.Result, paths []string) (id string, conflict bool, present bool) {
	for _, path := range paths {
		value := root.Get(path)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		present = true
		text := ""
		switch value.Type {
		case gjson.String:
			text = strings.TrimSpace(value.String())
		case gjson.Number:
			// 数字 ID 按原文保存，避免浮点转换丢失精度。
			text = strings.TrimSpace(value.Raw)
		default:
			return "", true, true
		}
		if text == "" {
			continue
		}
		if id != "" && id != text {
			return "", true, true
		}
		id = text
	}
	return id, false, present
}

// videoProfilePollRoot 定位查询响应中属于本任务的节点；列表中出现其它任务时整包拒绝。
func videoProfilePollRoot(spec VideoProtocolProfileSpec, body []byte, taskID string) (gjson.Result, bool) {
	root := gjson.ParseBytes(body)
	response := spec.Poll.Response
	// 依次尝试候选根节点，例如 MiniMax 中继的 task 封装；都不存在时使用整包。
	for _, path := range response.Root {
		if candidate := root.Get(path); candidate.IsObject() {
			root = candidate
			break
		}
	}
	if response.List != nil {
		list := root.Get(response.List.Path)
		if !list.IsArray() {
			if response.List.Optional {
				return root, true
			}
			return gjson.Result{}, taskID == ""
		}
		var selected gjson.Result
		found, sawID := false, false
		for _, item := range list.Array() {
			id, conflict, _ := videoProfileIDs(item, response.List.Match)
			if conflict {
				return gjson.Result{}, false
			}
			if id == "" && response.List.Optional {
				// 可选列表中没有任务 ID 的项是产物而不是任务，不参与归属判断。
				continue
			}
			sawID = true
			if id != taskID {
				return gjson.Result{}, false
			}
			selected, found = item, true
		}
		if !sawID && response.List.Optional {
			return root, true
		}
		return selected, found || taskID == ""
	}
	return root, true
}

// videoProfileResponseMatchesTask 校验响应属于冻结任务：创建响应不能含冲突 ID，查询响应的 ID 必须一致。
func videoProfileResponseMatchesTask(target VideoUpstreamTarget, body []byte, taskID string) bool {
	spec, err := videoProfileSpecFromTarget(target)
	if err != nil {
		return false
	}
	if !gjson.ValidBytes(body) {
		// 非 JSON 错误页没有任务归属，交由状态机按原规则处理。
		return true
	}
	if taskID == "" {
		_, conflict, _ := videoProfileIDs(gjson.ParseBytes(body), spec.Create.Response.TaskID)
		return !conflict
	}
	root, ok := videoProfilePollRoot(spec, body, taskID)
	if !ok {
		// 创建阶段已经取得 ID 时也可能用创建包重放归属。
		id, conflict, _ := videoProfileIDs(gjson.ParseBytes(body), spec.Create.Response.TaskID)
		return !conflict && id == taskID
	}
	paths := spec.Poll.Response.TaskID
	if len(paths) == 0 {
		return true
	}
	id, conflict, _ := videoProfileIDs(root, paths)
	if conflict {
		return false
	}
	if id == "" {
		// 查询包缺少 ID 时允许回退检查创建包格式，否则视为合法省略。
		createID, createConflict, _ := videoProfileIDs(gjson.ParseBytes(body), spec.Create.Response.TaskID)
		return !createConflict && (createID == "" || createID == taskID)
	}
	return id == taskID
}

// videoProfileHasConflictingTaskIDs 判断创建包是否给出多个不同的任务 ID。
func videoProfileHasConflictingTaskIDs(target VideoUpstreamTarget, body []byte) bool {
	spec, err := videoProfileSpecFromTarget(target)
	if err != nil {
		return true
	}
	if !gjson.ValidBytes(body) {
		return false
	}
	_, conflict, _ := videoProfileIDs(gjson.ParseBytes(body), spec.Create.Response.TaskID)
	return conflict
}

// extractVideoProfileResponse 把创建或查询响应解析为规范观测；未知状态不推进终态。
// 重复键、大小写歧义、非法或互相矛盾的计价观测都只标记元数据错误，不能据此扣费。
func extractVideoProfileResponse(target VideoUpstreamTarget, status int, body []byte, taskID string) (*VideoUpstreamResponse, error) {
	spec, err := videoProfileSpecFromTarget(target)
	if err != nil {
		return nil, err
	}
	out := &VideoUpstreamResponse{TaskID: taskID, StatusCode: status, Body: body}
	if !gjson.ValidBytes(body) || len(bytes.TrimSpace(body)) == 0 {
		return out, nil
	}
	full := gjson.ParseBytes(body)
	if taskID == "" {
		id, conflict, _ := videoProfileIDs(full, spec.Create.Response.TaskID)
		if conflict {
			out.MetadataError = "Conflicting video task identifiers"
			return out, nil
		}
		out.TaskID = id
		if id != "" {
			out.TaskRefs = map[string]string{}
			for name, path := range spec.Create.Response.Refs {
				if ref, _, _ := videoProfileIDs(full, []string{path}); ref != "" {
					out.TaskRefs[name] = ref
				}
			}
		} else if videoProfileRejected(full, spec.Create.Response.RejectedWhen) {
			// 只有模板明确声明的未受理条件，且没有任何任务 ID 时才视为失败释放。
			out.Status = "failed"
			return out, nil
		}
	}
	root, ok := videoProfilePollRoot(spec, body, out.TaskID)
	if !ok {
		return out, nil
	}
	response := spec.Poll.Response
	declared := []string{}
	for _, group := range [][]string{response.Status, response.VideoURL, response.Seconds, response.Tokens, response.Resolution} {
		declared = append(declared, group...)
	}
	if validateVideoJSONKeys(body) != nil || videoProfileCaseAmbiguous(root, declared) {
		// 同一字段出现多次或仅大小写不同，无法确定上游真实取值。
		out.MetadataError = "Ambiguous video response"
		return out, nil
	}
	for _, path := range response.Status {
		raw := root.Get(path)
		if raw.Type == gjson.String && strings.TrimSpace(raw.String()) != "" || raw.Type == gjson.Number {
			out.UpstreamStatus = strings.TrimSpace(raw.String())
			break
		}
	}
	for key, mapped := range response.StatusMap {
		if out.UpstreamStatus != "" && strings.EqualFold(key, out.UpstreamStatus) {
			out.Status = mapped
			break
		}
	}
	var sizes map[string]string
	if spec.Capabilities != nil {
		sizes = spec.Capabilities.SizeResolutions
	}
	fail := func(message string) (*VideoUpstreamResponse, error) {
		out.MetadataError, out.VideoURL = message, ""
		return out, nil
	}
	if out.VideoURL, ok = videoProfileObservedString(root, response.VideoURL, false, nil, false); !ok {
		return fail("Conflicting video result URLs")
	}
	seconds, valid := videoProfileObservedSeconds(root, response.Seconds, response.Strict)
	if !valid {
		return fail("Invalid or conflicting video output duration")
	}
	out.Metadata.DurationSeconds = seconds
	if response.Outputs != nil {
		// 产物数组：超过上限无法确定计价对象；声明了项内路径时直链与秒数只取唯一匹配项。
		spec := response.Outputs
		outputs := root.Get(spec.Path)
		itemFields := len(spec.URL) > 0 || len(spec.Seconds) > 0
		if outputs.Exists() && outputs.Type != gjson.Null && itemFields && !outputs.IsArray() {
			return fail("Video outputs must be an array")
		}
		matched := []gjson.Result{}
		for _, item := range outputs.Array() {
			keep := true
			for field, want := range spec.Filter {
				keep = keep && item.Get(field).Type == gjson.String && item.Get(field).String() == want
			}
			if keep {
				matched = append(matched, item)
			}
		}
		if len(matched) > spec.Max {
			return fail("Multiple video outputs require explicit pricing support")
		}
		if itemFields && outputs.IsArray() {
			if len(matched) == 1 {
				if len(spec.URL) > 0 {
					out.VideoURL, _ = videoProfileObservedString(matched[0], spec.URL, false, nil, false)
				}
				if len(spec.Seconds) > 0 {
					seconds, valid := videoProfileObservedSeconds(matched[0], spec.Seconds, response.Strict)
					if !valid {
						return fail("Invalid video output duration")
					}
					out.Metadata.DurationSeconds = seconds
				}
			}
			if out.Status == "completed" && (len(matched) != 1 || len(spec.URL) > 0 && out.VideoURL == "" || len(spec.Seconds) > 0 && out.Metadata.DurationSeconds <= 0) {
				return fail("Completed video task requires one output with URL and duration")
			}
		}
	}
	if out.VideoURL != "" && response.PublicURL && !validVideoYezonghuiURL(out.VideoURL) {
		return fail("Invalid video result URL")
	}
	tokens, valid := videoProfileObservedTokens(root, response.Tokens, response.Strict)
	if !valid {
		return fail("Invalid or conflicting video output tokens")
	}
	out.Metadata.Tokens = tokens
	if out.Metadata.Resolution, ok = videoProfileObservedString(root, response.Resolution, response.Strict, sizes, true); !ok {
		return fail("Invalid or conflicting video output resolution")
	}
	for _, path := range response.Resolution {
		// 经尺寸表换算的像素尺寸同时保留，供按尺寸计价。
		if raw := strings.TrimSpace(root.Get(path).String()); sizes[raw] != "" && sizes[raw] == out.Metadata.Resolution {
			out.Metadata.Size = raw
			break
		}
	}
	if out.Status == "completed" && videoProfileRejected(root, response.CompletedInvalidWhen) {
		// 上游声称完成却同时带错误或旧格式结果包，不能确认交付与计价。
		return fail("Video completion conflicts with an error or unexpected result envelope")
	}
	out.Metadata.Model = target.Model
	return out, nil
}

// videoProfileObservedString 读取字符串观测（直链或分辨率）；分辨率可为正整数（换算为 Np）或经尺寸表换算为档位。
// strict 时所有非空取值必须一致；非法类型一律视为无效。
func videoProfileObservedString(root gjson.Result, paths []string, strict bool, sizes map[string]string, resolution bool) (string, bool) {
	result := ""
	for _, path := range paths {
		value := root.Get(path)
		text := ""
		switch {
		case !value.Exists() || value.Type == gjson.Null:
			continue
		case value.Type == gjson.String:
			text = strings.TrimSpace(value.String())
		case resolution && value.Type == gjson.Number && value.Float() > 0 && value.Float() <= 65536 && math.Trunc(value.Float()) == value.Float():
			// 万相等协议以数字短边返回清晰度，例如 720 表示 720p。
			text = strconv.FormatInt(value.Int(), 10) + "p"
		default:
			return "", false
		}
		if text == "" {
			continue
		}
		if tier := sizes[text]; tier != "" {
			text = tier
		}
		if result == "" {
			result = text
			if !strict {
				break
			}
		} else if result != text {
			return "", false
		}
	}
	return result, true
}

// videoProfileObservedSeconds 读取实际输出秒数；接受正数或数字字符串，非法取值视为无效。
func videoProfileObservedSeconds(root gjson.Result, paths []string, strict bool) (float64, bool) {
	result := 0.0
	for _, path := range paths {
		value := root.Get(path)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		seconds, valid := videoProtocolObservedSeconds(value, true)
		if !valid {
			return 0, false
		}
		if result == 0 {
			result = seconds
			if !strict {
				break
			}
		} else if result != seconds {
			return 0, false
		}
	}
	return result, true
}

// videoProfileObservedTokens 读取输出 Token；零值在多数中转里只是占位，不能证明免费用量，按未知处理。
func videoProfileObservedTokens(root gjson.Result, paths []string, strict bool) (*int64, bool) {
	var result *int64
	for _, path := range paths {
		value := root.Get(path)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		if value.Type != gjson.Number || value.Float() < 0 || value.Float() >= 1e15 || math.Trunc(value.Float()) != value.Float() {
			return nil, false
		}
		n := value.Int()
		if n == 0 {
			continue
		}
		if result == nil {
			result = &n
			if !strict {
				break
			}
		} else if *result != n {
			return nil, false
		}
	}
	return result, true
}

// videoProfileCaseAmbiguous 判断声明路径上是否存在仅大小写不同的同名字段。
func videoProfileCaseAmbiguous(root gjson.Result, paths []string) bool {
	for _, path := range paths {
		node := root
		for _, segment := range strings.Split(path, ".") {
			if !node.IsObject() {
				break
			}
			ambiguous := false
			node.ForEach(func(key, _ gjson.Result) bool {
				if key.String() != segment && strings.EqualFold(key.String(), segment) {
					ambiguous = true
					return false
				}
				return true
			})
			if ambiguous {
				return true
			}
			node = node.Get(segment)
		}
	}
	return false
}

// videoProfileRejected 判断是否满足任一条件；JSON null 视为字段不存在。
func videoProfileRejected(root gjson.Result, conditions []VideoProfileCondition) bool {
	for _, condition := range conditions {
		value := root.Get(condition.Path)
		exists := value.Exists() && value.Type != gjson.Null
		if condition.Exists != nil {
			if exists == *condition.Exists {
				return true
			}
			continue
		}
		if !exists {
			continue
		}
		if len(condition.In) > 0 && videoProfileValueIn(value, condition.In) {
			return true
		}
		if len(condition.NotIn) > 0 && !videoProfileValueIn(value, condition.NotIn) {
			return true
		}
	}
	return false
}

func videoProfileValueIn(value gjson.Result, candidates []any) bool {
	for _, candidate := range candidates {
		switch c := candidate.(type) {
		case string:
			if value.Type == gjson.String && value.String() == c {
				return true
			}
		case float64:
			if value.Type == gjson.Number && value.Float() == c {
				return true
			}
		case bool:
			if (value.Type == gjson.True || value.Type == gjson.False) && value.Bool() == c {
				return true
			}
		}
	}
	return false
}

// videoProfileMaxProcessing 返回模板声明的处理中上限，未声明时使用平台默认值。
func videoProfileMaxProcessing(target VideoUpstreamTarget) int {
	if spec, err := videoProfileSpecFromTarget(target); err == nil && spec.Poll.MaxProcessingMinutes > 0 {
		return spec.Poll.MaxProcessingMinutes
	}
	return defaultVideoMaxProcessingMinutes
}
