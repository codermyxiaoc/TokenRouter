package service

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/tidwall/gjson"
)

// requiresOpenCodeDeepSeekSystemRole 只识别经 OpenCode 转发、映射后实际发送 DeepSeek 模型的 API Key 账号。
// DeepSeek 严格校验角色，收到 developer 会返回 422；OpenCode 复用多家供应商，其它模型不受影响。
// 判断依据是选中账号与目标地址及映射后的出站模型，不看客户端请求的别名。
// 同步上游 Sub2API 18bfa4bf2/ba5b21879 中的 OpenCode 分支；上游对 DeepSeek/Kimi/智谱平台与官方域名的
// 整体规范化不在本 fork 范围内，那些通道的原生 Chat 正文保持不变。
func requiresOpenCodeDeepSeekSystemRole(account *Account, targetURL, upstreamModel string) bool {
	if account == nil || account.Type != AccountTypeAPIKey {
		return false
	}
	if !account.IsOpenCodeGo() && !isOfficialOpenCodeHost(targetURL) {
		return false
	}
	return strings.HasPrefix(normalizeOpenCodeGoModelID(upstreamModel), "deepseek")
}

// normalizeOpenCodeDeepSeekDeveloperRoles 在选定账号与目标地址后，把 messages 中的 developer 角色改为 system。
// 不合并、不重排消息，也不把指令降级为 user；RawMessage 保留未知字段与数字精度。
// 输入正文不被修改，换号重试时其它账号仍收到原始 developer 角色。
// @project-doc docs/interfaces/openai_upstream.md#opencode_deepseek_roles
func normalizeOpenCodeDeepSeekDeveloperRoles(account *Account, targetURL string, body []byte) ([]byte, error) {
	if !requiresOpenCodeDeepSeekSystemRole(account, targetURL, gjson.GetBytes(body, "model").String()) {
		return body, nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return nil, errors.New("chat role normalization: invalid request object")
	}
	raw, exists := root["messages"]
	if !exists {
		return body, nil
	}
	var messages []json.RawMessage
	if json.Unmarshal(raw, &messages) != nil {
		return nil, errors.New("chat role normalization: invalid messages array")
	}
	changed := false
	for i, item := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(item, &message) != nil || message == nil {
			return nil, errors.New("chat role normalization: invalid message object")
		}
		var role string
		if json.Unmarshal(message["role"], &role) != nil {
			return nil, errors.New("chat role normalization: invalid message role")
		}
		if role != "developer" {
			continue
		}
		message["role"] = json.RawMessage(`"system"`)
		updated, err := json.Marshal(message)
		if err != nil {
			return nil, errors.New("chat role normalization: cannot encode message")
		}
		messages[i] = updated
		changed = true
	}
	// 没有 developer 时原样返回，避免无谓的重新编码改变正文字节。
	if !changed {
		return body, nil
	}
	updated, err := json.Marshal(messages)
	if err != nil {
		return nil, errors.New("chat role normalization: cannot encode messages")
	}
	root["messages"] = updated
	return json.Marshal(root)
}
