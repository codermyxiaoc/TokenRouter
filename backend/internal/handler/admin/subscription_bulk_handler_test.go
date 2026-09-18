package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 参数与幂等键必须在进入领域服务前验证，错误输入不能触碰订阅。
func TestSubscriptionBulkHandlersRejectInvalidRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &SubscriptionHandler{}
	router := gin.New()
	router.POST("/extend", h.BulkExtend)
	router.POST("/reset", h.BulkResetQuota)
	for _, tt := range []struct{ path, body, key string }{
		{"/extend", `{"subscription_ids":[],"days":1}`, "key"},
		{"/extend", `{"subscription_ids":[0],"days":1}`, "key"},
		{"/extend", `{"subscription_ids":[1],"days":0}`, "key"},
		{"/extend", `{"subscription_ids":[1],"days":1.5}`, "key"},
		{"/extend", `{"subscription_ids":[1],"days":36501}`, "key"},
		{"/extend", `{"subscription_ids":[1],"days":1}`, ""},
		{"/reset", `{"subscription_ids":[1]}`, "key"},
		{"/reset", `{"subscription_ids":[1],"daily":true}`, ""},
	} {
		req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", tt.key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, tt.body)
	}
}
