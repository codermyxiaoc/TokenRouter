package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// CyberSessionTranscriptBlock 是累计历史前缀 key 及其覆盖的历史 item 数。
// 记录长度后查询时可精确重算该前缀，长会话或填充续写仍能精确匹配。
type CyberSessionTranscriptBlock struct {
	Key    string
	Length int
}

// CyberSessionTranscriptBlocks 返回完整历史的精确屏蔽项，以及可选的、截止到最新用户轮次之前的
// 改写容忍屏蔽项。后者要求已出现模型生成的历史，避免共享的首轮模板误拦无关会话。
func CyberSessionTranscriptBlocks(apiKeyID int64, body []byte) []CyberSessionTranscriptBlock {
	var full, preLatestUser CyberSessionTranscriptBlock
	// 这是熵启发式而非来源证明：要完美区分固定的 few-shot assistant 条目需要服务端可信历史。
	hasModelGeneratedItem := false
	walkOpenAICyberTranscript(apiKeyID, body, func(item gjson.Result, length int, prefixKey func() string) bool {
		if openAICyberTranscriptItemStartsUserTurn(item) && hasModelGeneratedItem && full.Length > 0 {
			preLatestUser = full
		}
		full = CyberSessionTranscriptBlock{Key: prefixKey(), Length: length}
		if openAICyberTranscriptItemIsModelGenerated(item) {
			hasModelGeneratedItem = true
		}
		return true
	})
	if full.Length == 0 {
		return nil
	}
	blocks := []CyberSessionTranscriptBlock{full}
	if preLatestUser.Length > 0 {
		blocks = append(blocks, preLatestUser)
	}
	return blocks
}

// cyberSessionTranscriptKeysAt 在已记录的屏蔽长度处重算 transcript 前缀 key，按长度升序返回。
// 超出本次请求历史长度的记录不可能命中，直接跳过；遍历到最大记录长度即提前终止。
func cyberSessionTranscriptKeysAt(apiKeyID int64, body []byte, lengths []int) []string {
	wanted := make(map[int]struct{}, len(lengths))
	maxLength := 0
	for _, length := range lengths {
		if length > 0 {
			wanted[length] = struct{}{}
			maxLength = max(maxLength, length)
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	keys := make([]string, 0, len(wanted))
	walkOpenAICyberTranscript(apiKeyID, body, func(_ gjson.Result, length int, prefixKey func() string) bool {
		if _, ok := wanted[length]; ok {
			keys = append(keys, prefixKey())
		}
		return length < maxLength
	})
	return keys
}

// cyberSessionLegacyLookupKeys 是升级前屏蔽回退查询的前缀数量上限，与旧版查询规则一致。
const cyberSessionLegacyLookupKeys = 256

// cyberSessionRecentTranscriptKeys 返回最近 limit 个历史前缀 key（按长度升序），用于没有长度索引的旧屏蔽。
func cyberSessionRecentTranscriptKeys(apiKeyID int64, body []byte, limit int) []string {
	if limit <= 0 {
		return nil
	}
	ring := make([]string, 0, limit)
	next := 0
	walkOpenAICyberTranscript(apiKeyID, body, func(_ gjson.Result, _ int, prefixKey func() string) bool {
		if len(ring) < limit {
			ring = append(ring, prefixKey())
		} else {
			ring[next] = prefixKey()
			next = (next + 1) % limit
		}
		return true
	})
	if len(ring) < limit || next == 0 {
		return ring
	}
	return append(append(make([]string, 0, limit), ring[next:]...), ring[:next]...)
}

// walkOpenAICyberTranscript 累计哈希模型可见历史，每处理一个 item 后以从 1 开始的前缀长度调用 visit。
// prefixKey 返回截止该 item 的前缀 key，仅在本次回调期间有效；visit 返回 false 时停止遍历。
func walkOpenAICyberTranscript(apiKeyID int64, body []byte, visit func(item gjson.Result, length int, prefixKey func() string) bool) {
	if len(body) == 0 {
		return
	}
	root := openAIRequestPayloadView(body)
	if !root.Exists() || !root.IsObject() {
		return
	}

	h := sha256.New()
	_, _ = h.Write([]byte("cyber-transcript:v3|api_key="))
	_, _ = h.Write([]byte(strconv.FormatInt(apiKeyID, 10)))
	// 模型与工具定义属于请求配置而非历史；根级 instructions 是模型可见上下文，参与身份哈希。
	for _, field := range []string{"instructions"} {
		v := root.Get(field)
		if !v.Exists() || (v.Type == gjson.String && strings.TrimSpace(v.String()) == "") {
			continue
		}
		canonical := normalizeCompatSeedJSON(json.RawMessage(v.Raw))
		if v.Type == gjson.String {
			canonical = v.String()
		}
		_, _ = h.Write([]byte("|"))
		_, _ = h.Write([]byte(field))
		_, _ = h.Write([]byte("="))
		_, _ = h.Write([]byte(canonical))
	}

	prefixKey := func() string { return hex.EncodeToString(h.Sum(nil)) }
	length := 0
	feed := func(_, item gjson.Result) bool {
		canonical := item.Raw
		switch item.Type {
		case gjson.String:
			encoded, _ := json.Marshal(item.String())
			canonical = string(encoded)
		case gjson.JSON:
			canonical = normalizeCompatSeedJSON(json.RawMessage(item.Raw))
		}
		if strings.TrimSpace(canonical) == "" {
			return true
		}
		_, _ = h.Write([]byte("|item="))
		_, _ = h.Write([]byte(canonical))
		length++
		return visit(item, length, prefixKey)
	}

	if messages := root.Get("messages"); messages.Exists() {
		if messages.IsArray() {
			messages.ForEach(feed)
		}
		return
	}
	input := root.Get("input")
	if input.IsArray() {
		input.ForEach(feed)
		return
	}
	if input.Type == gjson.String && strings.TrimSpace(input.String()) != "" {
		feed(gjson.Result{}, input)
	}
}

func openAICyberTranscriptItemStartsUserTurn(item gjson.Result) bool {
	if strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "user") {
		content := item.Get("content")
		if content.IsArray() {
			hasUserContent := false
			content.ForEach(func(_, block gjson.Result) bool {
				switch strings.ToLower(strings.TrimSpace(block.Get("type").String())) {
				case "tool_result", "function_call_output", "custom_tool_call_output", "computer_call_output":
				default:
					hasUserContent = true
				}
				return !hasUserContent
			})
			return hasUserContent
		}
		return content.Exists()
	}
	return strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "input_text")
}

func openAICyberTranscriptItemIsModelGenerated(item gjson.Result) bool {
	switch strings.ToLower(strings.TrimSpace(item.Get("role").String())) {
	case "assistant", "model":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(item.Get("type").String())) {
	case "output_text", "function_call", "tool_call", "custom_tool_call", "computer_call":
		return true
	default:
		return false
	}
}
