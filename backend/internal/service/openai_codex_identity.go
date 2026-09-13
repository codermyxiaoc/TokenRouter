package service

import (
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/pkg/openai"
	"github.com/google/uuid"
)

// codexUpstreamMinVersion 上游 /backend-api/codex 接受的最低 version 头：
// 若请求携带 version 且低于该值，上游直接 404（issue #3901，2026-07 实测）。
const codexUpstreamMinVersion = "0.144.0"

const codexClientVersionMaxLen = 64

var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

var (
	codexCanonicalUAMu       sync.RWMutex
	codexCanonicalUAResolver func() string
)

// SetCodexCanonicalUserAgentResolver 注入后台设置提供的规范 Codex UA 解析器。
// 无法注入或解析失败时，所有无账号出站路径回退到编译期默认身份。
func SetCodexCanonicalUserAgentResolver(resolver func() string) {
	codexCanonicalUAMu.Lock()
	defer codexCanonicalUAMu.Unlock()
	codexCanonicalUAResolver = resolver
}

// CodexCanonicalUserAgent 返回当前生效的规范 Codex User-Agent。
func CodexCanonicalUserAgent() string {
	return resolveCodexOutboundIdentity("").userAgent
}

// CodexCanonicalAuthIdentity 返回凭据面使用的身份对；凭据面不需要 version 头。
func CodexCanonicalAuthIdentity() (userAgent, originator string) {
	identity := resolveCodexOutboundIdentity("")
	return identity.userAgent, identity.originator
}

// ApplyCodexCanonicalAuthIdentity 为凭据面请求写入规范 UA 与 originator。
func ApplyCodexCanonicalAuthIdentity(h http.Header) {
	if h == nil {
		return
	}
	userAgent, originator := CodexCanonicalAuthIdentity()
	h.Set("user-agent", userAgent)
	h.Set("originator", originator)
}

// CodexCanonicalClientVersion 返回与规范 UA 同源的版本号。
func CodexCanonicalClientVersion() string {
	return resolveCodexOutboundIdentity("").version
}

type codexOutboundIdentity struct {
	userAgent  string
	originator string
	version    string
}

func codexCanonicalUserAgent() string {
	codexCanonicalUAMu.RLock()
	resolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	if resolver != nil {
		if value := strings.TrimSpace(resolver()); value != "" {
			return value
		}
	}
	return codexCLIUserAgent
}

// NormalizeCodexClientVersion 只接受短 ASCII 版本号，避免异常值进入出站头。
func NormalizeCodexClientVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > codexClientVersionMaxLen || !codexClientVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

func resolveCodexOutboundIdentity(candidateUA string) codexOutboundIdentity {
	canonical := codexCanonicalUserAgent()
	ua := strings.TrimSpace(candidateUA)
	if ua == "" {
		ua = canonical
	}
	originator, pairedUA, ok := openai.PairCodexClientIdentity(ua)
	if !ok {
		originator, pairedUA, ok = openai.PairCodexClientIdentity(canonical)
	}
	if !ok {
		originator, pairedUA = openai.CodexDefaultOriginator, codexCLIUserAgent
	}
	version := codexClientVersionFromUA(canonical)
	if rebuilt := replaceCodexUserAgentVersion(pairedUA, version); rebuilt != "" {
		pairedUA = rebuilt
	}
	return codexOutboundIdentity{userAgent: pairedUA, originator: originator, version: version}
}

func codexClientVersionFromUA(userAgent string) string {
	version, ok := openai.ParseCodexEngineVersion(userAgent)
	if !ok {
		return codexCLIVersion
	}
	version = NormalizeCodexClientVersion(version)
	if version == "" || CompareVersions(version, codexUpstreamMinVersion) < 0 {
		return codexCLIVersion
	}
	return version
}

func replaceCodexUserAgentVersion(userAgent, version string) string {
	version = NormalizeCodexClientVersion(version)
	if version == "" {
		return ""
	}
	slash := strings.IndexByte(userAgent, '/')
	if slash <= 0 || slash+1 >= len(userAgent) {
		return ""
	}
	end := slash + 1
	for end < len(userAgent) && userAgent[end] != ' ' && userAgent[end] != '(' {
		end++
	}
	return userAgent[:slash+1] + version + userAgent[end:]
}

// ensureCodexIdentityHeaders 补齐 OAuth（ChatGPT 内部接口）出站请求所需的 Codex 身份头。
// 已有 User-Agent 与 version 保持不变，交给紧随其后的 enforceCodexIdentityHeaders
// 做官方身份配对与最低版本校正。
func ensureCodexIdentityHeaders(h http.Header) {
	if h == nil {
		return
	}
	identity := resolveCodexOutboundIdentity("")
	if strings.TrimSpace(h.Get("user-agent")) == "" {
		h.Set("user-agent", identity.userAgent)
	}
	if strings.TrimSpace(h.Get("originator")) == "" {
		h.Set("originator", identity.originator)
	}
	if strings.TrimSpace(h.Get("version")) == "" {
		h.Set("version", identity.version)
	}
	h.Set("OpenAI-Beta", "responses=experimental")
}

// applyOpenAICodexProbeHeaders 为合成 Responses 探测请求补齐 Codex 身份和窗口标识。
func applyOpenAICodexProbeHeaders(h http.Header) {
	if h == nil {
		return
	}
	ensureCodexIdentityHeaders(h)
	h.Set("X-Codex-Window-ID", uuid.NewString())
}

// enforceCodexIdentityHeaders 收口 OAuth（ChatGPT 内部接口）出站请求的客户端身份头。
// 上游要求 originator 与 User-Agent 首段配套且为官方客户端标识，version 头（若携带）
// 不低于 0.144.0，任一不满足即 404（issue #3901）。以最终 User-Agent 为准推导配套
// originator；推导不出官方身份（第三方 UA / UA 缺失）时整体回退为默认 Codex TUI 身份。
//
// 仅对携带 originator 的请求生效；需要从缺失身份头恢复的调用方应先调用
// ensureCodexIdentityHeaders。
// 必须在所有 User-Agent 改写（自定义 UA / ForceCodexCLI / 浏览器 UA 兜底）之后调用。
func enforceCodexIdentityHeaders(h http.Header) {
	enforceCodexIdentityHeadersWithUA(h, "")
}

// enforceCodexIdentityHeadersWithUA 保留官方 UA 的客户端名与设备指纹，
// 仅在无法配对或版本过旧时回退到 canonical 身份；overrideUA 供账号级配置调用方使用。
func enforceCodexIdentityHeadersWithUA(h http.Header, overrideUA string) {
	if h == nil || h.Get("originator") == "" {
		return
	}
	candidateUA := strings.TrimSpace(overrideUA)
	if candidateUA == "" {
		candidateUA = h.Get("user-agent")
	}
	originator, pairedUA, ok := openai.PairCodexClientIdentity(candidateUA)
	if !ok {
		identity := resolveCodexOutboundIdentity("")
		originator, pairedUA = identity.originator, identity.userAgent
	}
	h.Set("user-agent", pairedUA)
	h.Set("originator", originator)
	if v := strings.TrimSpace(h.Get("version")); v != "" && CompareVersions(v, codexUpstreamMinVersion) < 0 {
		h.Set("version", CodexCanonicalClientVersion())
	}
}
