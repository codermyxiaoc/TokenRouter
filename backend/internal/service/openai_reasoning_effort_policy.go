package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	maxReasoningEffortMappings = 64
	maxReasoningEffortValueLen = 64
	maxReasoningEffortModelLen = 200

	// ReasoningEffortOverLimitDowngrade 表示超出上限时改写为上限值。
	ReasoningEffortOverLimitDowngrade = "downgrade"
	// ReasoningEffortOverLimitDeny 表示超出上限时拒绝请求。
	ReasoningEffortOverLimitDeny = "deny"
)

var (
	// openAIReasoningEffortValues 是可用于分组上限的有序档位；none 没有强度排名，
	// 因此不能作为上限值。
	openAIReasoningEffortValues = []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	// anthropicReasoningEffortValues 是 Anthropic output_config.effort 支持的档位。
	anthropicReasoningEffortValues = []string{"low", "medium", "high", "xhigh", "max"}
	// openAIReasoningEffortMappingValues 是映射规则允许的显式输入/输出值。
	// 保留 none 使管理员能为不同上游自行配置兼容策略。
	openAIReasoningEffortMappingValues = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	// Anthropic 不接受 OpenAI 专用的 minimal/none 映射值。
	anthropicReasoningEffortMappingValues = anthropicReasoningEffortValues
)

type requestedReasoningEffortContextKey struct{}

type openAIReasoningEffortPolicyContextKey struct{}

// openAIReasoningEffortPolicy 保存请求级推理强度策略快照，避免异步转发期间
// 读取到已经被调用方修改的分组切片。
type openAIReasoningEffortPolicy struct {
	maxEffort    string
	overLimit    string
	requestModel string
	mappings     []ReasoningEffortMapping
}

// ReasoningEffortOverLimitError 表示显式推理强度超过分组上限且策略要求拒绝。
type ReasoningEffortOverLimitError struct {
	Requested string
	Max       string
}

func (e *ReasoningEffortOverLimitError) Error() string {
	if e == nil {
		return "reasoning effort exceeds this group's limit"
	}
	requested := strings.TrimSpace(e.Requested)
	max := strings.TrimSpace(e.Max)
	if requested == "" && max == "" {
		return "reasoning effort exceeds this group's limit"
	}
	if requested == "" {
		return fmt.Sprintf("reasoning effort exceeds this group's limit of %q", max)
	}
	if max == "" {
		return fmt.Sprintf("reasoning effort %q exceeds this group's limit", requested)
	}
	return fmt.Sprintf("reasoning effort %q exceeds this group's limit of %q", requested, max)
}

// WithRequestedReasoningEffort 将请求进入策略层前捕获的客户端档位绑定到 context。
func WithRequestedReasoningEffort(ctx context.Context, effort string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, requestedReasoningEffortContextKey{}, effort)
}

// RequestedReasoningEffortFromContext 读取已绑定的客户端档位。
func RequestedReasoningEffortFromContext(ctx context.Context) *string {
	if ctx == nil {
		return nil
	}
	effort, ok := ctx.Value(requestedReasoningEffortContextKey{}).(string)
	if !ok {
		return nil
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil
	}
	return &effort
}

// NormalizeMaxReasoningEffort 校验并标准化分组策略值。
// 空字符串表示分组不设置上限。
func NormalizeMaxReasoningEffort(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	switch value {
	case "":
		return ""
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "extrahigh":
		return "xhigh"
	case "max":
		return "max"
	default:
		return ""
	}
}

// normalizeReasoningEffortMappingValue 标准化映射规则使用的值。none 只表示
// 上游协议中的关闭推理状态，不能参与分组上限的强度比较。
func normalizeReasoningEffortMappingValue(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	if value == "none" {
		return "none"
	}
	return NormalizeMaxReasoningEffort(value)
}

// normalizeRequestedOpenAIReasoningEffort 仅用于记录客户端显式请求值。none 没有
// 可比较的强度排名，不能作为分组上限，但必须保留在请求审计中。
func normalizeRequestedOpenAIReasoningEffort(raw string) string {
	return normalizeReasoningEffortMappingValue(raw)
}

func reasoningEffortValuesForPlatform(platform string) []string {
	switch platform {
	case PlatformOpenAI:
		return openAIReasoningEffortValues
	case PlatformAnthropic:
		return anthropicReasoningEffortValues
	default:
		return nil
	}
}

func reasoningEffortMappingValuesForPlatform(platform string) []string {
	switch platform {
	case PlatformOpenAI:
		return openAIReasoningEffortMappingValues
	case PlatformAnthropic:
		return anthropicReasoningEffortMappingValues
	default:
		return nil
	}
}

func normalizeMaxReasoningEffortForPlatform(platform, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	allowedValues := reasoningEffortValuesForPlatform(platform)
	if len(allowedValues) == 0 {
		return "", fmt.Errorf("reasoning effort policy is only supported for platforms %q and %q", PlatformAnthropic, PlatformOpenAI)
	}

	value := NormalizeMaxReasoningEffort(raw)
	for _, allowed := range allowedValues {
		if value == allowed {
			return value, nil
		}
	}
	return "", fmt.Errorf(
		"reasoning effort %q is not supported for platform %q; allowed values: %s",
		raw,
		platform,
		strings.Join(allowedValues, ", "),
	)
}

func normalizeReasoningEffortMappingValueForPlatform(platform, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	allowedValues := reasoningEffortMappingValuesForPlatform(platform)
	if len(allowedValues) == 0 {
		return "", fmt.Errorf("reasoning effort policy is only supported for platforms %q and %q", PlatformAnthropic, PlatformOpenAI)
	}

	value := normalizeReasoningEffortMappingValue(raw)
	for _, allowed := range allowedValues {
		if value == allowed {
			return value, nil
		}
	}
	return "", fmt.Errorf(
		"reasoning effort mapping value %q is not supported for platform %q; allowed values: %s",
		raw,
		platform,
		strings.Join(allowedValues, ", "),
	)
}

// NormalizeMaxReasoningEffortOverLimit 将超限动作归一化；空值兼容历史默认降档。
func NormalizeMaxReasoningEffortOverLimit(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", ReasoningEffortOverLimitDowngrade:
		return ReasoningEffortOverLimitDowngrade
	case ReasoningEffortOverLimitDeny:
		return ReasoningEffortOverLimitDeny
	default:
		return ""
	}
}

// normalizeMaxReasoningEffortOverLimitForPlatform 校验动作是否能用于目标平台。
// 降档是无副作用的兼容默认值；拒绝仅对当前 fork 支持的 OpenAI 分组开放。
func normalizeMaxReasoningEffortOverLimitForPlatform(platform, raw string) (string, error) {
	value := NormalizeMaxReasoningEffortOverLimit(raw)
	if value == "" {
		return "", fmt.Errorf(
			"reasoning effort over-limit action %q is not supported; allowed values: %s, %s",
			raw,
			ReasoningEffortOverLimitDowngrade,
			ReasoningEffortOverLimitDeny,
		)
	}
	if value == ReasoningEffortOverLimitDowngrade {
		return value, nil
	}
	if platform != PlatformAnthropic && platform != PlatformOpenAI {
		return "", fmt.Errorf(
			"reasoning effort over-limit deny is only supported for platforms %q and %q",
			PlatformAnthropic,
			PlatformOpenAI,
		)
	}
	return value, nil
}

func reasoningEffortRank(raw string) (int, bool) {
	switch NormalizeMaxReasoningEffort(raw) {
	case "minimal":
		return 1, true
	case "low":
		return 2, true
	case "medium":
		return 3, true
	case "high":
		return 4, true
	case "xhigh":
		return 5, true
	case "max":
		return 6, true
	default:
		return 0, false
	}
}

// NormalizeReasoningEffortMappings 根据 OpenAI 分组支持的档位及协议关闭值，
// 校验并标准化分组映射规则。
func normalizeReasoningEffortMatchType(matchType, model string) (string, error) {
	model = strings.TrimSpace(model)
	matchType = strings.ToLower(strings.TrimSpace(matchType))
	if model == "" {
		return "", nil
	}
	switch matchType {
	case "", domain.ReasoningEffortMatchExact:
		return domain.ReasoningEffortMatchExact, nil
	case domain.ReasoningEffortMatchPrefix:
		return domain.ReasoningEffortMatchPrefix, nil
	case domain.ReasoningEffortMatchSuffix:
		return domain.ReasoningEffortMatchSuffix, nil
	default:
		return "", fmt.Errorf("invalid match_type %q", matchType)
	}
}

func reasoningEffortMappingDuplicateKey(from, matchType, model string) string {
	return from + "\x00" + matchType + "\x00" + strings.ToLower(strings.TrimSpace(model))
}

func requestModelMatchesReasoningEffortMapping(requestModel, matchType, mappingModel string) bool {
	scope := strings.ToLower(strings.TrimSpace(mappingModel))
	req := strings.ToLower(strings.TrimSpace(requestModel))
	switch matchType {
	case "":
		return true
	case domain.ReasoningEffortMatchExact:
		return scope != "" && scope == req
	case domain.ReasoningEffortMatchPrefix:
		return scope != "" && req != "" && strings.HasPrefix(req, scope)
	case domain.ReasoningEffortMatchSuffix:
		return scope != "" && req != "" && strings.HasSuffix(req, scope)
	default:
		return false
	}
}

func selectReasoningEffortMapping(mappings []ReasoningEffortMapping, from, requestModel string) (ReasoningEffortMapping, bool) {
	type candidate struct {
		mapping       ReasoningEffortMapping
		matchStrength int
		patternLen    int
		index         int
	}
	candidates := make([]candidate, 0, len(mappings))
	for i, mapping := range mappings {
		if normalizeReasoningEffortMappingValue(mapping.From) != from {
			continue
		}
		model := strings.TrimSpace(mapping.Model)
		matchType, err := normalizeReasoningEffortMatchType(mapping.MatchType, model)
		if err != nil {
			continue
		}
		if !requestModelMatchesReasoningEffortMapping(requestModel, matchType, model) {
			continue
		}
		strength := 1
		patternLen := 0
		switch matchType {
		case domain.ReasoningEffortMatchExact:
			strength = 3
		case domain.ReasoningEffortMatchPrefix, domain.ReasoningEffortMatchSuffix:
			strength = 2
			patternLen = len(strings.ToLower(model))
		}
		candidates = append(candidates, candidate{
			mapping:       mapping,
			matchStrength: strength,
			patternLen:    patternLen,
			index:         i,
		})
	}
	if len(candidates) == 0 {
		return ReasoningEffortMapping{}, false
	}
	best := candidates[0]
	for _, item := range candidates[1:] {
		if item.matchStrength != best.matchStrength {
			if item.matchStrength > best.matchStrength {
				best = item
			}
			continue
		}
		if item.patternLen != best.patternLen {
			if item.patternLen > best.patternLen {
				best = item
			}
			continue
		}
		if item.index < best.index {
			best = item
		}
	}
	return best.mapping, true
}

func NormalizeReasoningEffortMappings(platform string, raw []ReasoningEffortMapping) ([]ReasoningEffortMapping, error) {
	if len(raw) > maxReasoningEffortMappings {
		return nil, fmt.Errorf("reasoning effort mappings cannot exceed %d entries", maxReasoningEffortMappings)
	}

	normalized := make([]ReasoningEffortMapping, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, mapping := range raw {
		from := normalizeReasoningEffortMappingValue(mapping.From)
		to := normalizeReasoningEffortMappingValue(mapping.To)
		if from == "" || to == "" {
			return nil, fmt.Errorf("reasoning effort mapping %d contains an empty or unknown value", i+1)
		}
		if len(from) > maxReasoningEffortValueLen || len(to) > maxReasoningEffortValueLen {
			return nil, fmt.Errorf("reasoning effort mapping %d values cannot exceed %d characters", i+1, maxReasoningEffortValueLen)
		}
		if _, err := normalizeReasoningEffortMappingValueForPlatform(platform, from); err != nil {
			return nil, fmt.Errorf("reasoning effort mapping %d source: %w", i+1, err)
		}
		if _, err := normalizeReasoningEffortMappingValueForPlatform(platform, to); err != nil {
			return nil, fmt.Errorf("reasoning effort mapping %d target: %w", i+1, err)
		}
		model := strings.TrimSpace(mapping.Model)
		if len(model) > maxReasoningEffortModelLen {
			return nil, fmt.Errorf("reasoning effort mapping %d model cannot exceed %d characters", i+1, maxReasoningEffortModelLen)
		}
		matchType, err := normalizeReasoningEffortMatchType(mapping.MatchType, model)
		if err != nil {
			return nil, fmt.Errorf("reasoning effort mapping %d: %w", i+1, err)
		}
		key := reasoningEffortMappingDuplicateKey(from, matchType, model)
		if _, exists := seen[key]; exists {
			if model == "" {
				return nil, fmt.Errorf("duplicate reasoning effort mapping source %q", from)
			}
			return nil, fmt.Errorf("duplicate reasoning effort mapping source %q for %s model %q", from, matchType, strings.ToLower(model))
		}
		seen[key] = struct{}{}
		normalized = append(normalized, ReasoningEffortMapping{
			From:      from,
			To:        to,
			MatchType: matchType,
			Model:     model,
		})
	}
	return normalized, nil
}

// WithOpenAIReasoningEffortPolicy 将分组策略绑定到请求上下文。
func WithOpenAIReasoningEffortPolicy(ctx context.Context, maxEffort string, mappings []ReasoningEffortMapping, overLimit string) context.Context {
	return withOpenAIReasoningEffortPolicyForModel(ctx, maxEffort, mappings, overLimit, "")
}

// WithOpenAIReasoningEffortPolicyForModel 绑定策略并保留客户端模型，供模型范围映射使用。
func WithOpenAIReasoningEffortPolicyForModel(ctx context.Context, maxEffort string, mappings []ReasoningEffortMapping, overLimit, requestModel string) context.Context {
	return withOpenAIReasoningEffortPolicyForModel(ctx, maxEffort, mappings, overLimit, requestModel)
}

func withOpenAIReasoningEffortPolicyForModel(ctx context.Context, maxEffort string, mappings []ReasoningEffortMapping, overLimit, requestModel string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIReasoningEffortPolicyContextKey{}, openAIReasoningEffortPolicy{
		maxEffort:    maxEffort,
		overLimit:    overLimit,
		requestModel: strings.TrimSpace(requestModel),
		mappings:     append([]ReasoningEffortMapping(nil), mappings...),
	})
}

// ApplyOpenAIReasoningEffortPolicyFromContext 执行先前绑定的请求级策略。
func ApplyOpenAIReasoningEffortPolicyFromContext(ctx context.Context, body []byte) ([]byte, bool, error) {
	if ctx == nil {
		return body, false, nil
	}
	policy, ok := ctx.Value(openAIReasoningEffortPolicyContextKey{}).(openAIReasoningEffortPolicy)
	if !ok {
		return body, false, nil
	}
	return applyOpenAIReasoningEffortPolicy(body, policy.maxEffort, policy.mappings, policy.overLimit, policy.requestModel)
}

func mapReasoningEffort(raw string, mappings []ReasoningEffortMapping, requestModel string) (string, bool) {
	value := strings.TrimSpace(raw)
	canonical := normalizeReasoningEffortMappingValue(value)
	if canonical == "" {
		return value, false
	}
	mapping, ok := selectReasoningEffortMapping(mappings, canonical, requestModel)
	if !ok {
		return value, false
	}
	return strings.TrimSpace(mapping.To), true
}

func sanitizeGroupReasoningEffortPolicy(group *Group) {
	if group == nil {
		return
	}
	maxEffort, maxErr := normalizeMaxReasoningEffortForPlatform(group.Platform, group.MaxReasoningEffort)
	mappings, mappingsErr := NormalizeReasoningEffortMappings(group.Platform, group.ReasoningEffortMappings)
	if maxErr != nil {
		maxEffort = ""
	}
	if mappingsErr != nil {
		mappings = []ReasoningEffortMapping{}
	}
	overLimit := NormalizeMaxReasoningEffortOverLimit(group.MaxReasoningEffortOverLimit)
	if overLimit == "" || (overLimit == ReasoningEffortOverLimitDeny && group.Platform != PlatformAnthropic && group.Platform != PlatformOpenAI) {
		overLimit = ReasoningEffortOverLimitDowngrade
	}
	group.MaxReasoningEffort = maxEffort
	group.MaxReasoningEffortOverLimit = overLimit
	group.ReasoningEffortMappings = mappings
}

// ApplyOpenAIReasoningEffortPolicy 先应用模型范围映射，再按配置降档或拒绝超限请求。
// 未指定的值保持不变，继续由上游默认值控制。
func ApplyOpenAIReasoningEffortPolicy(body []byte, maxEffort string, mappings []ReasoningEffortMapping, overLimit string) ([]byte, bool, error) {
	return applyOpenAIReasoningEffortPolicy(body, maxEffort, mappings, overLimit, "")
}

func applyOpenAIReasoningEffortPolicy(body []byte, maxEffort string, mappings []ReasoningEffortMapping, overLimit, requestModel string) ([]byte, bool, error) {
	maxRank, hasMax := reasoningEffortRank(maxEffort)
	if len(body) == 0 || (!hasMax && len(mappings) == 0) {
		return body, false, nil
	}

	if strings.TrimSpace(requestModel) == "" {
		requestModel = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	}
	deny := hasMax && NormalizeMaxReasoningEffortOverLimit(overLimit) == ReasoningEffortOverLimitDeny
	canonicalMax := NormalizeMaxReasoningEffort(maxEffort)
	result := body
	changed := false
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		field := gjson.GetBytes(result, path)
		if !field.Exists() || field.Type != gjson.String {
			continue
		}
		original := strings.TrimSpace(field.String())
		if original == "" {
			continue
		}

		effective, _ := mapReasoningEffort(original, mappings, requestModel)
		if currentRank, recognized := reasoningEffortRank(effective); recognized {
			effective = NormalizeMaxReasoningEffort(effective)
			if hasMax && currentRank > maxRank {
				if deny {
					return body, false, &ReasoningEffortOverLimitError{Requested: effective, Max: canonicalMax}
				}
				effective = canonicalMax
			}
		}
		if effective == original {
			continue
		}

		updated, err := sjson.SetBytes(result, path, effective)
		if err != nil {
			continue
		}
		result = updated
		changed = true
	}
	return result, changed, nil
}

// applyOpenAIWSReasoningEffortPolicy 将同一套分组策略应用到 WS 请求帧。
// requestModel 由调用方提供客户端模型，支持后续省略 model 的多轮帧。
func applyOpenAIWSReasoningEffortPolicy(payload []byte, hooks *OpenAIWSIngressHooks, requestModel string) ([]byte, error) {
	if hooks == nil || (hooks.MaxReasoningEffort == "" && len(hooks.ReasoningEffortMappings) == 0) {
		return payload, nil
	}
	updated, changed, err := applyOpenAIReasoningEffortPolicy(
		payload,
		hooks.MaxReasoningEffort,
		hooks.ReasoningEffortMappings,
		hooks.MaxReasoningEffortOverLimit,
		strings.TrimSpace(requestModel),
	)
	if err != nil {
		return payload, err
	}
	if changed {
		return updated, nil
	}
	return payload, nil
}
