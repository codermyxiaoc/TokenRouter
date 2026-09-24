package service

import (
	"errors"
	"fmt"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

const (
	defaultGroupAvailabilityProbeIntervalMinutes = 30
	defaultGroupAvailabilityProbeTimeoutSeconds  = 30
	defaultGroupAvailabilityProbeMaxRetries      = 3
	minGroupAvailabilityProbeIntervalMinutes     = 1
	maxGroupAvailabilityProbeIntervalMinutes     = 1440
	minGroupAvailabilityProbeTimeoutSeconds      = 5
	maxGroupAvailabilityProbeTimeoutSeconds      = 120
	minGroupAvailabilityProbeMaxRetries          = 0
	maxGroupAvailabilityProbeMaxRetries          = 10
	maxGroupAvailabilityProbeUserAgentLength     = 512
)

const invalidGroupAvailabilityProbeConfigReason = "INVALID_AVAILABILITY_PROBE_CONFIG"

const (
	GroupAvailabilityProbeProtocolAuto   = "auto"
	GroupAvailabilityProbeProtocolGemini = "gemini"
)

// normalizeGroupAvailabilityProbeProtocol 将历史空值视为自动模式，拒绝未实现的协议。
func normalizeGroupAvailabilityProbeProtocol(protocol string) (string, error) {
	protocol = strings.TrimSpace(protocol)
	switch protocol {
	case "", GroupAvailabilityProbeProtocolAuto:
		return GroupAvailabilityProbeProtocolAuto, nil
	case APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic, GroupAvailabilityProbeProtocolGemini:
		return protocol, nil
	default:
		return "", errors.New("availability_probe_config.protocol is unsupported")
	}
}

// ValidateGroupAvailabilityProbeProtocol 校验平台原生探测能力，避免保存后静默改测其它协议。
// 账号类型和模型相关限制由实际执行阶段继续校验。
func ValidateGroupAvailabilityProbeProtocol(platform, protocol string) error {
	normalized, err := normalizeGroupAvailabilityProbeProtocol(protocol)
	if err != nil {
		return err
	}
	if normalized == GroupAvailabilityProbeProtocolAuto {
		return nil
	}
	supported := false
	switch platform {
	case PlatformOpenAI:
		supported = normalized == APIProtocolChatCompletions || normalized == APIProtocolResponses
	case PlatformKimi, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo:
		supported = normalized == APIProtocolChatCompletions || normalized == APIProtocolResponses || normalized == APIProtocolAnthropic
	case PlatformZhipu:
		supported = normalized == APIProtocolChatCompletions || normalized == APIProtocolAnthropic
	case PlatformAnthropic:
		supported = normalized == APIProtocolAnthropic
	case PlatformGemini:
		supported = normalized == GroupAvailabilityProbeProtocolGemini
	case PlatformGrok:
		supported = normalized == APIProtocolResponses
	}
	if !supported {
		return fmt.Errorf("availability_probe_config.protocol %q is not supported for platform %q", normalized, platform)
	}
	return nil
}

// normalizeGroupAvailabilityProbeConfig 统一清洗分组主动探测配置。
// 未启用时只保留 enabled=false，避免无效模型和提示词长期堆积在 JSON 字段里。
func normalizeGroupAvailabilityProbeConfig(cfg GroupAvailabilityProbeConfig) (GroupAvailabilityProbeConfig, error) {
	if !cfg.Enabled {
		return GroupAvailabilityProbeConfig{}, nil
	}

	cfg.ModelID = strings.TrimSpace(cfg.ModelID)
	cfg.Prompt = strings.TrimSpace(cfg.Prompt)
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	protocol, err := normalizeGroupAvailabilityProbeProtocol(cfg.Protocol)
	if err != nil {
		return GroupAvailabilityProbeConfig{}, err
	}
	cfg.Protocol = protocol
	if cfg.ModelID == "" {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.model_id is required when enabled")
	}
	if cfg.Prompt == "" {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.prompt is required when enabled")
	}
	if len(cfg.UserAgent) > maxGroupAvailabilityProbeUserAgentLength {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.user_agent is too long")
	}
	if hasInvalidHTTPHeaderValueByte(cfg.UserAgent) {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.user_agent contains invalid header characters")
	}

	if cfg.IntervalMinutes == 0 {
		cfg.IntervalMinutes = defaultGroupAvailabilityProbeIntervalMinutes
	}
	if cfg.IntervalMinutes < minGroupAvailabilityProbeIntervalMinutes || cfg.IntervalMinutes > maxGroupAvailabilityProbeIntervalMinutes {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.interval_minutes must be between 1 and 1440")
	}

	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = defaultGroupAvailabilityProbeTimeoutSeconds
	}
	if cfg.TimeoutSeconds < minGroupAvailabilityProbeTimeoutSeconds || cfg.TimeoutSeconds > maxGroupAvailabilityProbeTimeoutSeconds {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.timeout_seconds must be between 5 and 120")
	}

	// 指针用于区分旧配置缺失字段与管理员显式设置 0 次重试。
	maxRetries := defaultGroupAvailabilityProbeMaxRetries
	if cfg.MaxRetries != nil {
		maxRetries = *cfg.MaxRetries
	}
	if maxRetries < minGroupAvailabilityProbeMaxRetries || maxRetries > maxGroupAvailabilityProbeMaxRetries {
		return GroupAvailabilityProbeConfig{}, errors.New("availability_probe_config.max_retries must be between 0 and 10")
	}
	cfg.MaxRetries = &maxRetries

	return cfg, nil
}

// normalizeGroupAvailabilityProbeConfigForAdminWrite 将管理端输入错误转换为稳定的 HTTP 400 契约。
func normalizeGroupAvailabilityProbeConfigForAdminWrite(cfg GroupAvailabilityProbeConfig) (GroupAvailabilityProbeConfig, error) {
	normalized, err := normalizeGroupAvailabilityProbeConfig(cfg)
	if err != nil {
		return GroupAvailabilityProbeConfig{}, infraerrors.BadRequest(invalidGroupAvailabilityProbeConfigReason, err.Error())
	}
	return normalized, nil
}

// hasInvalidHTTPHeaderValueByte 拒绝控制字符，避免保存后在发送 User-Agent header 时失败。
func hasInvalidHTTPHeaderValueByte(value string) bool {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}
