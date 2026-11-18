package service

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAINativeCompactionV2Key 标记本请求是原生 remote compaction v2。handler
// 在识别出真实 wire 形状后写入，出站请求构造器据此补齐协商头。
const openAINativeCompactionV2Key = "openai_native_compaction_v2"

const openAIRemoteCompactionV2Feature = "remote_compaction_v2"

// MarkOpenAINativeCompactionV2 标记当前请求为原生 V2 压缩协议。
func MarkOpenAINativeCompactionV2(c *gin.Context) {
	if c != nil {
		c.Set(openAINativeCompactionV2Key, true)
	}
}

// NormalizeCompactionTriggerInputOrder keeps a single compaction trigger as
// the final Responses input item, as required by the upstream v2 wire format.
func NormalizeCompactionTriggerInputOrder(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	if !compactionTriggerNeedsDecode(body) {
		return body, false, nil
	}
	var payload map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &payload); err != nil {
		return body, false, err
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) == 0 {
		return body, false, nil
	}
	triggerCount := 0
	normalized := make([]any, 0, len(input))
	for _, raw := range input {
		item, itemOK := raw.(map[string]any)
		if itemOK && item["type"] == "compaction_trigger" {
			triggerCount++
			continue
		}
		normalized = append(normalized, raw)
	}
	if triggerCount == 0 {
		return body, false, nil
	}
	if triggerCount == 1 {
		if last, ok := input[len(input)-1].(map[string]any); ok && last["type"] == "compaction_trigger" {
			return body, false, nil
		}
	}
	normalized = append(normalized, map[string]any{"type": "compaction_trigger"})
	payload["input"] = normalized
	encoded, err := marshalOpenAIUpstreamJSON(payload)
	if err != nil {
		return body, false, err
	}
	return encoded, true, nil
}

var compactionTriggerToken = []byte("compaction_trigger")

// compactionTriggerNeedsDecode 让不可能包含 trigger 的请求体跳过整包解码：
// type 字符串除非用 \u 转义拼写，否则必然字面出现。跳过解码的请求体仍须是合法 JSON 对象，
// 保证非法请求体继续从原解码路径报错。
// 没有拼出纯 ASCII 的 \u 转义时，解码得到的每个 "compaction_trigger" 字符串都是该记号的字面出现：
// 没有出现即没有 trigger；仅出现一次且（经零拷贝视图读取）恰是最后一个 input 项的 type 时，
// 解码器看到的要么是唯一的 trigger，要么因重复成员遮蔽而没有 trigger，两种情况请求体都原样返回。
func compactionTriggerNeedsDecode(body []byte) bool {
	if JSONMayEscapePlainASCII(body) {
		return true
	}
	switch bytes.Count(body, compactionTriggerToken) {
	case 0:
		trimmed := bytes.TrimLeft(body, " \t\r\n")
		return len(trimmed) == 0 || trimmed[0] != '{' || !gjson.ValidBytes(body)
	case 1:
		return !compactionTriggerIsLastInputItem(body) || !gjson.ValidBytes(body)
	default:
		return true
	}
}

// compactionTriggerIsLastInputItem reports whether the last element of the
// top-level input array is an object whose type is "compaction_trigger".
func compactionTriggerIsLastInputItem(body []byte) bool {
	input := parseRawJSONView(body).Get("input")
	if !input.IsArray() {
		return false
	}
	var last gjson.Result
	input.ForEach(func(_, item gjson.Result) bool {
		last = item
		return true
	})
	return last.IsObject() && last.Get("type").String() == "compaction_trigger"
}

func isOpenAINativeCompactionV2(c *gin.Context) bool {
	return c != nil && c.GetBool(openAINativeCompactionV2Key)
}

// IsOpenAINativeCompactionV2 返回当前请求是否被识别为原生远程 compaction v2。
func IsOpenAINativeCompactionV2(c *gin.Context) bool {
	return isOpenAINativeCompactionV2(c)
}

// ensureOpenAIRemoteCompactionV2BetaFeature 确保协商头包含 V2 能力，同时保留
// 客户端已声明的其它能力，避免把多行头压缩时丢失 token。
func ensureOpenAIRemoteCompactionV2BetaFeature(h http.Header) {
	if h == nil {
		return
	}
	tokens := make([]string, 0, 4)
	for _, value := range h.Values("x-codex-beta-features") {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if token == openAIRemoteCompactionV2Feature {
				return
			}
			tokens = append(tokens, token)
		}
	}
	tokens = append(tokens, openAIRemoteCompactionV2Feature)
	h.Set("x-codex-beta-features", strings.Join(tokens, ","))
}

func hasOpenAICodexBetaFeaturesHeader(h http.Header) bool {
	if h == nil {
		return false
	}
	for _, value := range h.Values("x-codex-beta-features") {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// applyOpenAICodexBetaFeatures 对齐 Codex 的会话级协商行为：原生 V2 压缩请求
// 总会带 remote_compaction_v2；其它 OAuth Responses 请求仅在客户端未声明时补
// 默认能力。非 OAuth 上游不接收额外会话级能力头。
func applyOpenAICodexBetaFeatures(c *gin.Context, account *Account, h http.Header) {
	if h == nil {
		return
	}
	if isOpenAINativeCompactionV2(c) {
		ensureOpenAIRemoteCompactionV2BetaFeature(h)
		return
	}
	if account == nil || !account.IsOpenAIOAuthLike() {
		return
	}
	if hasOpenAICodexBetaFeaturesHeader(h) {
		return
	}
	h.Set("x-codex-beta-features", openAIRemoteCompactionV2Feature)
}

// HasCompactionTriggerInInput 检测 input 中 type="compaction_trigger" 的条目。
// handler 会结合请求路径和 stream 字段，区分原生 remote compaction v2 流式协议
// 与旧的 /responses/compact 桥接协议。
// type 字符串除非用拼出纯 ASCII 的 \u 转义，否则必然字面出现，其它请求体可跳过结构扫描。
func HasCompactionTriggerInInput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if !bytes.Contains(body, compactionTriggerToken) && !JSONMayEscapePlainASCII(body) {
		return false
	}
	input := parseRawJSONView(body).Get("input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "compaction_trigger" {
			found = true
			return false
		}
		return true
	})
	return found
}
