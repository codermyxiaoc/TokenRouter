package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 账号映射只改变模型，需在最终 HTTP 边界同步处理能力头。
// 不修改入站头和正文，避免故障切换到原生账号时丢失 Lite 能力。
func applyMappedGPT55LiteCompatibility(req *http.Request, account *Account, body []byte) error {
	if req == nil || account == nil || !account.IsOpenAIOAuthLike() {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(body, "model").String()) != "gpt-5.5" {
		return nil
	}
	if !isOpenAIResponsesLiteHeader(req.Header.Get(responsesLiteHeader)) && !isOpenAIResponsesLiteWebSocketPayload(body) {
		return nil
	}
	// 非 Lite Codex 端点仍支持 additional_tools、命名空间和 all_turns。
	// 保留这些字段以及全部历史和工具结果。
	if isOpenAIResponsesLiteWebSocketPayload(body) {
		var err error
		body, err = sjson.DeleteBytes(body, "client_metadata."+responsesLiteWSMetadataKey)
		if err != nil {
			return fmt.Errorf("remove mapped GPT-5.5 Lite metadata: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		savedBody := append([]byte(nil), body...)
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(savedBody)), nil }
	}
	req.Header.Del(responsesLiteHeader)
	return nil
}
