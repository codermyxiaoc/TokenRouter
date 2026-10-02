//go:build unit

package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/handler/dto"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 渠道和分组的真实 JSON 绑定均保留三种视频模式、非预设分辨率、零价和独立固定附加费。
func TestVideoAdminReadinessPricingHTTPAndDTO(t *testing.T) {
	for _, mode := range []string{"video", "video_token", "video_per_request"} {
		t.Run(mode, func(t *testing.T) {
			prepay := ""
			if mode == "video_token" {
				prepay = `,"video_token_prepay":{"price_per_second":0.3}`
			}
			pricing := fmt.Sprintf(`{"platform":"video","models":["video-model"],"billing_mode":%q,"price_multiplier":2,"video_prices":[{"resolution":"480p","price":0},{"resolution":"768p","price":23}],"video_fallback_price":42,"video_image_input_pricing":{"free_images":5,"price":0.15}%s}`, mode, prepay)
			bind := func(body string, target any) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				require.NoError(t, c.ShouldBindJSON(target))
			}
			var channelRequest channelModelPricingRequest
			bind(pricing, &channelRequest)
			entries := pricingRequestToService([]channelModelPricingRequest{channelRequest})
			raw, err := json.Marshal(pricingToResponse(&entries[0]))
			require.NoError(t, err)
			check := func(raw []byte) {
				require.Equal(t, mode, gjson.GetBytes(raw, "billing_mode").String())
				require.Equal(t, "video", gjson.GetBytes(raw, "platform").String())
				require.Len(t, gjson.GetBytes(raw, "video_prices").Array(), 2)
				require.True(t, gjson.GetBytes(raw, "video_prices.0.price").Exists())
				require.Zero(t, gjson.GetBytes(raw, "video_prices.0.price").Float())
				require.Equal(t, "768p", gjson.GetBytes(raw, "video_prices.1.resolution").String())
				require.Equal(t, 23., gjson.GetBytes(raw, "video_prices.1.price").Float())
				require.Equal(t, 42., gjson.GetBytes(raw, "video_fallback_price").Float())
				require.Equal(t, .15, gjson.GetBytes(raw, "video_image_input_pricing.price").Float())
				require.Equal(t, int64(5), gjson.GetBytes(raw, "video_image_input_pricing.free_images").Int())
				if mode == "video_token" {
					require.Equal(t, .3, gjson.GetBytes(raw, "video_token_prepay.price_per_second").Float())
				} else {
					require.False(t, gjson.GetBytes(raw, "video_token_prepay").Exists())
				}
			}
			check(raw)
			body := `{"name":"video","platform":"video","video_rate_independent":true,"video_rate_multiplier":0.5,"model_pricing":[` + pricing + `]}`
			var create CreateGroupRequest
			bind(body, &create)
			var update UpdateGroupRequest
			bind(body, &update)
			require.NotNil(t, update.ModelPricing)
			require.Equal(t, create.ModelPricing, *update.ModelPricing)
			group := dto.GroupFromServiceAdmin(&service.Group{Platform: create.Platform, ModelPricing: create.ModelPricing,
				VideoRateIndependent: create.VideoRateIndependent, VideoRateMultiplier: *create.VideoRateMultiplier})
			raw, err = json.Marshal(group)
			require.NoError(t, err)
			check([]byte(gjson.GetBytes(raw, "model_pricing.0").Raw))
			require.True(t, gjson.GetBytes(raw, "video_rate_independent").Bool())
			require.Equal(t, .5, gjson.GetBytes(raw, "video_rate_multiplier").Float())
		})
	}
}

// 导出文件重新经 HTTP 导入时，Video 身份和多端点配置必须原样交给公共创建校验，不能变成 Gemini。
func TestVideoAdminReadinessAccountExportImportRoundTrip(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	credentials := map[string]any{
		"api_key": "fixture-export-only", "base_url": "https://video.example",
		"video_endpoints":         []any{"compat", "seedance"},
		"video_model_bindings":    map[string]any{"upstream-model": []any{"compat", "seedance"}},
		"video_base_urls":         map[string]any{"seedance": "https://ark.example"},
		"model_mapping":           map[string]any{"client-model": "upstream-model"},
		"video_max_pending_tasks": float64(10), "video_max_duration_seconds": float64(15),
	}
	adminSvc.accounts = []service.Account{{ID: 21, Name: "video-export", Platform: service.PlatformVideo, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Credentials: credentials, Concurrency: 3}}
	exported := httptest.NewRecorder()
	router.ServeHTTP(exported, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data?include_proxies=false", nil))
	require.Equal(t, http.StatusOK, exported.Code)
	var exportData struct {
		Data DataPayload `json:"data"`
	}
	require.NoError(t, json.Unmarshal(exported.Body.Bytes(), &exportData))
	require.Len(t, exportData.Data.Accounts, 1)
	skip := true
	body, err := json.Marshal(DataImportRequest{Data: exportData.Data, SkipDefaultGroupBind: &skip})
	require.NoError(t, err)
	imported := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(imported, request)
	require.Equal(t, http.StatusOK, imported.Code, imported.Body.String())
	require.Equal(t, int64(1), gjson.GetBytes(imported.Body.Bytes(), "data.account_created").Int())
	require.Len(t, adminSvc.createdAccounts, 1)
	input := adminSvc.createdAccounts[0]
	require.Equal(t, service.PlatformVideo, input.Platform)
	require.Equal(t, service.AccountTypeAPIKey, input.Type)
	require.Equal(t, credentials, input.Credentials)
	require.True(t, input.SkipDefaultGroupBind)
	require.Empty(t, input.GroupIDs, "导入文件不能暗中绑定来源实例的分组 ID")
}
