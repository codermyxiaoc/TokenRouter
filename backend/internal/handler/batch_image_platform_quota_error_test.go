//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 批量接口保留平台专用错误码、重置时间和可执行的重试退避，不再返回模糊内部错误。
func TestBatchImagePlatformQuotaErrorMetadata(t *testing.T) {
	for _, quotaErr := range []*infraerrors.ApplicationError{service.ErrUserPlatformDailyQuotaExhausted, service.ErrUserPlatformWeeklyQuotaExhausted, service.ErrUserPlatformMonthlyQuotaExhausted} {
		t.Run(quotaErr.Reason, func(t *testing.T) {
			reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			batchImageError(ctx, fmt.Errorf("reserve: %w", quotaErr.WithMetadata(map[string]string{"window_resets_at": reset})))
			require.Equal(t, 429, recorder.Code)
			seconds, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
			require.NoError(t, err)
			require.Greater(t, seconds, 3500)
			require.LessOrEqual(t, seconds, 3600)
			var body struct {
				Error struct {
					Code     string            `json:"code"`
					Metadata map[string]string `json:"metadata"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.Equal(t, quotaErr.Reason, body.Error.Code)
			require.Equal(t, reset, body.Error.Metadata["window_resets_at"])
		})
	}
}
