package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// CyberSessionBlockStore 是 cyber 会话屏蔽表的存取接口。
// repository 层 gatewayCache 通过类型断言接入，测试桩未实现时自动降级为关闭。
// ErrCyberSessionLegacyScope 表示来源 scope 激活但没有长度索引（升级前写入的屏蔽）。
// 调用方应回退到逐长度计算前缀 key，保证升级前仍有效的屏蔽不被静默放行。
var ErrCyberSessionLegacyScope = errors.New("cyber session scope has no transcript length index")

type CyberSessionBlockStore interface {
	SetCyberSessionBlocked(ctx context.Context, block CyberSessionBlock, ttl time.Duration) error
	// CyberSessionTranscriptLengths 返回该 API Key 已记录的 transcript 屏蔽长度；来源 scope 未激活时返回空。
	CyberSessionTranscriptLengths(ctx context.Context, apiKeyID int64, scopeKey string) ([]int, error)
	FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error)
}

// CyberSessionBlock 描述一次需要持久化的 cyber 命中。Keys 是精确屏蔽 key；
// TranscriptLengths 记录其中各 transcript key 覆盖的历史长度，与 ScopeKey 一样只用于 transcript 匹配。
type CyberSessionBlock struct {
	APIKeyID          int64
	ScopeKey          string
	Keys              []string
	TranscriptLengths []int
}

// legacyCyberSessionBlockStore 兼容迁移前缓存接口，避免旧部署在升级后静默失去会话屏蔽。
type legacyCyberSessionBlockStore interface {
	SetCyberSessionBlocked(ctx context.Context, key string, ttl time.Duration) error
	IsCyberSessionBlocked(ctx context.Context, key string) (bool, error)
}

type legacyCyberSessionBlockStoreAdapter struct{ legacy legacyCyberSessionBlockStore }

func (a legacyCyberSessionBlockStoreAdapter) SetCyberSessionBlocked(ctx context.Context, block CyberSessionBlock, ttl time.Duration) error {
	key := strings.TrimSpace(block.ScopeKey)
	if key == "" && len(block.Keys) > 0 {
		key = strings.TrimSpace(block.Keys[0])
	}
	if key == "" {
		return nil
	}
	return a.legacy.SetCyberSessionBlocked(ctx, key, ttl)
}

// CyberSessionTranscriptLengths 旧缓存接口无法记录 transcript 屏蔽长度，因此不参与 transcript 前缀匹配。
// 旧接口写入时只持久化单个 key（scope 或首个 key），从未保存 transcript key，返回空不会削弱原有拦截。
func (a legacyCyberSessionBlockStoreAdapter) CyberSessionTranscriptLengths(context.Context, int64, string) ([]int, error) {
	return nil, nil
}

func (a legacyCyberSessionBlockStoreAdapter) FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error) {
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		blocked, err := a.legacy.IsCyberSessionBlocked(ctx, key)
		if err != nil {
			return "", err
		}
		if blocked {
			return key, nil
		}
	}
	return "", nil
}

// CyberSessionExplicitBlockKey returns an inexpensive exact key when the
// client supplies a stable session signal.
func CyberSessionExplicitBlockKey(apiKeyID int64, c *gin.Context, body []byte) string {
	return hashCyberSessionBlockKey(apiKeyID, explicitOpenAISessionID(c, body))
}

// CyberSessionScopeKey is a coarse, non-blocking fingerprint used only to
// avoid transcript parsing and MGET for sources that never produced a hit.
func CyberSessionScopeKey(apiKeyID int64, clientIP, userAgent string) string {
	if apiKeyID <= 0 {
		return ""
	}
	raw := "cyber-scope:v1|api_key=" + strconv.FormatInt(apiKeyID, 10) +
		"|ip=" + strings.TrimSpace(clientIP) +
		"|ua=" + NormalizeSessionUserAgent(userAgent)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func hashCyberSessionBlockKey(apiKeyID int64, raw string) string {
	if raw == "" {
		return ""
	}
	isolated := isolateOpenAISessionID(apiKeyID, raw)
	sum := sha256.Sum256([]byte(isolated))
	return hex.EncodeToString(sum[:])
}

// cyberSessionBlockStore 探测网关缓存是否支持 cyber 会话屏蔽。
func (s *OpenAIGatewayService) cyberSessionBlockStore() CyberSessionBlockStore {
	if s == nil || s.cache == nil {
		return nil
	}
	if store, ok := s.cache.(CyberSessionBlockStore); ok {
		return store
	}
	if legacy, ok := s.cache.(legacyCyberSessionBlockStore); ok {
		return legacyCyberSessionBlockStoreAdapter{legacy: legacy}
	}
	return nil
}

// CyberSessionBlockRuntime 返回会话屏蔽开关和 TTL，默认关闭。
func (s *OpenAIGatewayService) CyberSessionBlockRuntime(ctx context.Context) (bool, time.Duration) {
	if s == nil || s.settingService == nil {
		return false, time.Hour
	}
	return s.settingService.GetCyberSessionBlockRuntime(ctx)
}

// MarkCyberSessionBlocked 把会话写入屏蔽表（写入点：cyber 命中后）。
// 开关关闭、key 为空或存储不可用时静默跳过。
func (s *OpenAIGatewayService) MarkCyberSessionBlocked(ctx context.Context, block CyberSessionBlock) {
	if s == nil || len(block.Keys) == 0 {
		return
	}
	enabled, ttl := s.CyberSessionBlockRuntime(ctx)
	if !enabled {
		return
	}
	store := s.cyberSessionBlockStore()
	if store == nil {
		return
	}
	if err := store.SetCyberSessionBlocked(ctx, block, ttl); err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber session block write failed: err=%v", err)
	}
}

// FindCyberSessionBlockedForRequest applies explicit-first lookup followed by
// scope-gated transcript matching. All failures remain fail-open.
func (s *OpenAIGatewayService) FindCyberSessionBlockedForRequest(ctx context.Context, apiKeyID int64, c *gin.Context, body []byte, clientIP, userAgent string) string {
	enabled, _ := s.CyberSessionBlockRuntime(ctx)
	if !enabled {
		return ""
	}
	store := s.cyberSessionBlockStore()
	if store == nil {
		return ""
	}
	if explicitKey := CyberSessionExplicitBlockKey(apiKeyID, c, body); explicitKey != "" {
		key, err := store.FindCyberSessionBlocked(ctx, []string{explicitKey})
		if err != nil {
			logger.LegacyPrintf("service.openai_gateway", "cyber explicit session read failed: err=%v", err)
			return ""
		}
		if key != "" {
			return key
		}
	}
	lengths, err := store.CyberSessionTranscriptLengths(ctx, apiKeyID, CyberSessionScopeKey(apiKeyID, clientIP, userAgent))
	var keys []string
	switch {
	case errors.Is(err, ErrCyberSessionLegacyScope):
		// 升级前的屏蔽没有长度索引：按旧版规则查询最近的有界前缀，精确 key 的哈希算法未变，仍能命中。
		keys = cyberSessionRecentTranscriptKeys(apiKeyID, body, cyberSessionLegacyLookupKeys)
	case err != nil:
		logger.LegacyPrintf("service.openai_gateway", "cyber session scope read failed: err=%v", err)
		return ""
	default:
		// 只在已记录的屏蔽长度处重算前缀 key：追加任意数量 item 仍能命中被屏蔽前缀，
		// 而历史长度本身不再决定是否屏蔽，避免无关长会话被误拦。
		keys = cyberSessionTranscriptKeysAt(apiKeyID, body, lengths)
	}
	if len(keys) == 0 {
		return ""
	}
	key, err := store.FindCyberSessionBlocked(ctx, keys)
	if err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber session block batch read failed: err=%v", err)
		return ""
	}
	return key
}
