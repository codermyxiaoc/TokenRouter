package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsRequestPayloadHandlerRepo struct {
	service.OpsRepository
	detail *service.OpsRequestPayloadDetail
	calls  int
	id     string
}

func (r *opsRequestPayloadHandlerRepo) GetRequestPayloadDetail(_ context.Context, requestID string) (*service.OpsRequestPayloadDetail, error) {
	r.calls++
	r.id = requestID
	return r.detail, nil
}

// 管理端读取完整失败请求时，保留长正文；非法标识与禁用状态不能触达存储。
func TestOpsRequestPayloadHandlerBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requestBody := `{"input":"` + strings.Repeat("x", service.OpsRequestPayloadMaxBytes+1024) + `"}`
	for _, tc := range []struct {
		name          string
		requestID     string
		missing       bool
		disabled      bool
		wantStatus    int
		wantRepoCalls int
	}{
		{name: "完整正文", requestID: "req-1", wantStatus: http.StatusOK, wantRepoCalls: 1},
		{name: "无历史快照", requestID: "req-missing", missing: true, wantStatus: http.StatusNotFound, wantRepoCalls: 1},
		{name: "非法标识", requestID: strings.Repeat("x", 201), wantStatus: http.StatusBadRequest},
		{name: "运维关闭", requestID: "req-1", disabled: true, wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &opsRequestPayloadHandlerRepo{}
			if !tc.missing {
				repo.detail = &service.OpsRequestPayloadDetail{RequestID: tc.requestID, RequestBody: requestBody}
			}
			svc := service.NewOpsService(repo, nil, &config.Config{Ops: config.OpsConfig{Enabled: !tc.disabled}}, nil, nil, nil, nil, nil, nil, nil, nil)
			h := NewOpsHandler(svc)
			router := gin.New()
			router.GET("/requests/:request_id/detail", h.GetRequestPayloadDetail)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/requests/"+tc.requestID+"/detail", nil))
			require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			require.Equal(t, tc.wantRepoCalls, repo.calls)
			if tc.wantStatus == http.StatusOK {
				var envelope struct {
					Data service.OpsRequestPayloadDetail `json:"data"`
				}
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
				require.Equal(t, tc.requestID, repo.id)
				require.Equal(t, requestBody, envelope.Data.RequestBody)
			}
		})
	}
}
