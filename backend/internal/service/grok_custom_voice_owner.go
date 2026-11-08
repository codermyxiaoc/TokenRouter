package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

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

// grokVoiceOwnerPending 保存归属写入失败、尚未持久化的新声音（声音 ID → 创建者）。
// 没有记录会被视为历史声音并放行，因此写入失败时必须在本进程内继续拒绝他人，并在后台重试持久化。
var grokVoiceOwnerPending sync.Map

// grokVoiceOwnerWriteAttempts 是同步写入归属的尝试次数；写入使用独立上下文，客户端断开不会中断。
const grokVoiceOwnerWriteAttempts = 3

// grokVoiceOwnerRetryDelays 是同步失败后后台持久化归属的退避间隔。
var grokVoiceOwnerRetryDelays = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}

// recordGrokCustomVoiceOwner 可靠记录新声音的创建者：先用独立上下文同步重试，仍失败时登记为本进程待持久化，
// 期间他人访问一律拒绝，并在后台继续写入 Redis。
func recordGrokCustomVoiceOwner(ctx context.Context, cache GrokCustomVoiceOwnerCache, voiceID string, userID int64) {
	write := func() error {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		return cache.SetGrokCustomVoiceOwner(writeCtx, voiceID, userID)
	}
	var err error
	for attempt := 0; attempt < grokVoiceOwnerWriteAttempts; attempt++ {
		if err = write(); err == nil {
			grokVoiceOwnerPending.Delete(voiceID)
			return
		}
	}
	grokVoiceOwnerPending.Store(voiceID, userID)
	slog.Error("grok_custom_voice_owner_write_failed", "voice_id", voiceID, "user_id", userID, "error", err)
	go func() {
		for _, delay := range grokVoiceOwnerRetryDelays {
			time.Sleep(delay)
			if write() == nil {
				grokVoiceOwnerPending.Delete(voiceID)
				slog.Info("grok_custom_voice_owner_write_recovered", "voice_id", voiceID, "user_id", userID)
				return
			}
		}
		slog.Error("grok_custom_voice_owner_write_abandoned", "voice_id", voiceID, "user_id", userID)
	}()
}

// pendingGrokCustomVoiceOwner 返回本进程内尚未持久化的创建者。
func pendingGrokCustomVoiceOwner(voiceID string) int64 {
	if v, ok := grokVoiceOwnerPending.Load(voiceID); ok {
		owner, _ := v.(int64)
		return owner
	}
	return 0
}

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
	// 归属写入失败、尚未持久化的新声音先按本进程记录判断，不能落入“无记录即历史声音”的放行分支。
	if owner := pendingGrokCustomVoiceOwner(voiceID); owner > 0 {
		if owner != userID {
			return &grokCustomVoiceAccessError{status: http.StatusNotFound}
		}
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
			recordGrokCustomVoiceOwner(ctx, cache, voiceID, userID)
		}
	case len(parts) == 2 && method == http.MethodDelete:
		grokVoiceOwnerPending.Delete(parts[1])
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
		voiceID := strings.TrimSpace(voice.Get("voice_id").String())
		if pendingGrokCustomVoiceOwner(voiceID) == userID {
			kept = append(kept, json.RawMessage(voice.Raw))
			continue
		}
		owner, err := cache.GetGrokCustomVoiceOwner(ctx, voiceID)
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
