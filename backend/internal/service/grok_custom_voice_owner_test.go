//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// voiceOwnerCacheStub 只实现归属能力；嵌入 GatewayCache 满足网关缓存类型。
type voiceOwnerCacheStub struct {
	GatewayCache
	owners map[string]int64
	err    error
}

func (s *voiceOwnerCacheStub) SetGrokCustomVoiceOwner(_ context.Context, id string, userID int64) error {
	s.owners[id] = userID
	return nil
}
func (s *voiceOwnerCacheStub) GetGrokCustomVoiceOwner(_ context.Context, id string) (int64, error) {
	return s.owners[id], s.err
}
func (s *voiceOwnerCacheStub) DeleteGrokCustomVoiceOwner(_ context.Context, id string) error {
	delete(s.owners, id)
	return nil
}

func voiceTestContext(method string, userID int64) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(method, "/v1/custom-voices", nil)
	c.Set("api_key", &APIKey{ID: 1, UserID: userID})
	return c
}

// 非创建者不能访问、修改、删除或在 TTS 中引用他人声音；创建者与无归属记录的声音保持原行为。
func TestGrokCustomVoiceOwnershipAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := &voiceOwnerCacheStub{owners: map[string]int64{"v-alice": 1}}
	svc := &OpenAIGatewayService{cache: cache}
	ctx := context.Background()

	var accessErr *grokCustomVoiceAccessError
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		err := svc.authorizeGrokCustomVoice(ctx, voiceTestContext(method, 2), []string{"custom-voices", "v-alice"}, nil)
		require.True(t, errors.As(err, &accessErr), method)
		require.Equal(t, http.StatusNotFound, accessErr.status)
	}
	require.Error(t, svc.authorizeGrokCustomVoice(ctx, voiceTestContext(http.MethodGet, 2), []string{"custom-voices", "v-alice", "audio"}, nil))
	require.Error(t, svc.authorizeGrokCustomVoice(ctx, voiceTestContext(http.MethodPost, 2), []string{"tts"}, []byte(`{"input":"hi","voice_id":"v-alice"}`)))

	require.NoError(t, svc.authorizeGrokCustomVoice(ctx, voiceTestContext(http.MethodGet, 1), []string{"custom-voices", "v-alice"}, nil))
	require.NoError(t, svc.authorizeGrokCustomVoice(ctx, voiceTestContext(http.MethodPost, 2), []string{"tts"}, []byte(`{"input":"hi","voice":"eve"}`)), "内置声音无归属记录")
	require.NoError(t, svc.authorizeGrokCustomVoice(ctx, voiceTestContext(http.MethodGet, 2), []string{"custom-voices", "legacy"}, nil), "上线前创建的声音无记录")

	// 归属存储故障时拒绝，不放开他人声音。
	cache.err = errors.New("redis down")
	err := svc.authorizeGrokCustomVoice(ctx, voiceTestContext(http.MethodGet, 1), []string{"custom-voices", "v-alice"}, nil)
	require.True(t, errors.As(err, &accessErr))
	require.Equal(t, http.StatusServiceUnavailable, accessErr.status)
}

// 创建时记录归属，列表只返回当前用户创建的声音，删除时清理归属。
func TestGrokCustomVoiceOwnershipLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := &voiceOwnerCacheStub{owners: map[string]int64{"v-bob": 3}}
	svc := &OpenAIGatewayService{cache: cache}
	ctx := context.Background()

	svc.applyGrokCustomVoiceOwnership(ctx, voiceTestContext(http.MethodPost, 1), []string{"custom-voices"}, []byte(`{"voice_id":"v-new","name":"n"}`))
	require.Equal(t, int64(1), cache.owners["v-new"])

	list := svc.applyGrokCustomVoiceOwnership(ctx, voiceTestContext(http.MethodGet, 1), []string{"custom-voices"},
		[]byte(`{"voices":[{"voice_id":"v-new"},{"voice_id":"v-bob"},{"voice_id":"legacy"}],"pagination_token":null}`))
	ids := gjson.GetBytes(list, "voices.#.voice_id").Array()
	require.Len(t, ids, 1)
	require.Equal(t, "v-new", ids[0].String())
	require.True(t, gjson.GetBytes(list, "pagination_token").Exists(), "保留列表其它字段")

	svc.applyGrokCustomVoiceOwnership(ctx, voiceTestContext(http.MethodDelete, 1), []string{"custom-voices", "v-new"}, nil)
	_, exists := cache.owners["v-new"]
	require.False(t, exists)

	// 未实现归属能力的缓存保持原透传行为。
	plain := &OpenAIGatewayService{}
	body := []byte(`{"voices":[{"voice_id":"v-bob"}]}`)
	require.Equal(t, body, plain.applyGrokCustomVoiceOwnership(ctx, voiceTestContext(http.MethodGet, 1), []string{"custom-voices"}, body))
	require.NoError(t, plain.authorizeGrokCustomVoice(ctx, voiceTestContext(http.MethodGet, 2), []string{"custom-voices", "v-bob"}, nil))
}
