package openai

import (
	"github.com/TokenFlux/TokenRouter/internal/pkg/jsonutil"
	"github.com/tidwall/gjson"
)

// CacheWriteTTLTokens 从 Responses 或 Chat 用量中读取 30 分钟与 12 小时的写入量。
// 两个计数必须都存在且精确加总为本次写入总量，否则沿用统一写入价。
func CacheWriteTTLTokens(usage gjson.Result, total int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	for _, path := range []string{"input_tokens_details.cache_write_tokens_details", "prompt_tokens_details.cache_write_tokens_details"} {
		details := usage.Get(path)
		if !details.Exists() {
			continue
		}
		// 期限只是总写入的可选证据，存在重复项或形状错误时继续按可信总量计费。
		if !details.IsObject() || jsonutil.ValidateUniqueFields([]byte(details.Raw), "ttl_30m_tokens", "ttl_12h_tokens") != nil {
			return 0, 0
		}
		short, okShort := jsonutil.ParseNonNegativeInt(details.Get("ttl_30m_tokens").Raw, total)
		long, okLong := jsonutil.ParseNonNegativeInt(details.Get("ttl_12h_tokens").Raw, total)
		if okShort && okLong && long == total-short {
			return short, long
		}
		break
	}
	return 0, 0
}
