package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// GrokCustomVoiceOwnerCache 记录自定义声音的创建者。同一分组的用户共用上游账号，
// 上游不区分本站用户，因此归属只能在本站维护；未实现该接口的缓存保持原有透传行为。
type GrokCustomVoiceOwnerCache interface {
	SetGrokCustomVoiceOwner(ctx context.Context, voiceID string, userID int64) error
	// GetGrokCustomVoiceOwner 返回创建者；没有记录（本功能上线前创建或内置声音）时返回 0。
	GetGrokCustomVoiceOwner(ctx context.Context, voiceID string) (int64, error)
	DeleteGrokCustomVoiceOwner(ctx context.Context, voiceID string) error
}

// errGrokCustomVoiceNotFound 对非创建者统一表现为不存在，不泄露声音是否存在。
type grokCustomVoiceAccessError struct{ status int }

func (e *grokCustomVoiceAccessError) Error() string { return "custom voice not found" }

func (s *OpenAIGatewayService) grokCustomVoiceOwnerCache() GrokCustomVoiceOwnerCache {
	if s == nil || s.cache == nil {
		return nil
	}
	cache, _ := s.cache.(GrokCustomVoiceOwnerCache)
	return cache
}

func grokVoiceRequestUserID(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	if apiKey := getAPIKeyFromContext(c); apiKey != nil {
		return apiKey.UserID
	}
	return 0
}

// authorizeGrokCustomVoice 在请求上游前校验声音归属：
// 访问单个声音（查询/修改/删除/下载）或在 TTS 中引用他人创建的声音时拒绝；无归属记录时保持原行为。
func (s *OpenAIGatewayService) authorizeGrokCustomVoice(ctx context.Context, c *gin.Context, parts []string, body []byte) error {
	cache := s.grokCustomVoiceOwnerCache()
	userID := grokVoiceRequestUserID(c)
	if cache == nil || userID <= 0 {
		return nil
	}
	voiceID := ""
	switch {
	case parts[0] == "custom-voices" && len(parts) >= 2:
		voiceID = parts[1]
	case parts[0] == "tts" && gjson.ValidBytes(body):
		for _, field := range []string{"voice_id", "voice"} {
			if v := strings.TrimSpace(gjson.GetBytes(body, field).String()); v != "" {
				voiceID = v
				break
			}
		}
	}
	if voiceID == "" {
		return nil
	}
	owner, err := cache.GetGrokCustomVoiceOwner(ctx, voiceID)
	if err != nil {
		// 归属无法确认时拒绝，避免存储故障期间放开他人声音。
		return &grokCustomVoiceAccessError{status: http.StatusServiceUnavailable}
	}
	if owner > 0 && owner != userID {
		return &grokCustomVoiceAccessError{status: http.StatusNotFound}
	}
	return nil
}

// applyGrokCustomVoiceOwnership 在上游成功后维护归属，并把声音列表收窄为当前用户创建的声音。
func (s *OpenAIGatewayService) applyGrokCustomVoiceOwnership(ctx context.Context, c *gin.Context, parts []string, data []byte) []byte {
	cache := s.grokCustomVoiceOwnerCache()
	userID := grokVoiceRequestUserID(c)
	if cache == nil || userID <= 0 || parts[0] != "custom-voices" || c == nil || c.Request == nil {
		return data
	}
	method := c.Request.Method
	switch {
	case len(parts) == 1 && method == http.MethodPost:
		if voiceID := strings.TrimSpace(gjson.GetBytes(data, "voice_id").String()); voiceID != "" {
			_ = cache.SetGrokCustomVoiceOwner(ctx, voiceID, userID)
		}
	case len(parts) == 2 && method == http.MethodDelete:
		_ = cache.DeleteGrokCustomVoiceOwner(ctx, parts[1])
	case len(parts) == 1 && method == http.MethodGet:
		return filterGrokCustomVoiceList(ctx, cache, userID, data)
	}
	return data
}

// filterGrokCustomVoiceList 只保留当前用户创建的声音，避免同分组用户枚举他人声音。
func filterGrokCustomVoiceList(ctx context.Context, cache GrokCustomVoiceOwnerCache, userID int64, data []byte) []byte {
	voices := gjson.GetBytes(data, "voices")
	if !voices.IsArray() {
		return data
	}
	kept := make([]json.RawMessage, 0, len(voices.Array()))
	for _, voice := range voices.Array() {
		owner, err := cache.GetGrokCustomVoiceOwner(ctx, strings.TrimSpace(voice.Get("voice_id").String()))
		if err == nil && owner == userID {
			kept = append(kept, json.RawMessage(voice.Raw))
		}
	}
	filtered, err := sjson.SetBytes(data, "voices", kept)
	if err != nil {
		return data
	}
	return filtered
}
