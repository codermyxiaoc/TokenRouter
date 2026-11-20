package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/httputil"
	"github.com/TokenFlux/TokenRouter/internal/pkg/jsonutil"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/TokenFlux/TokenRouter/internal/service"

	"github.com/gin-gonic/gin"
)

const gatewayRequestBodyLimitKey = "gateway_request_body_limit"

// RequestBodyLimit 使用 MaxBytesReader 限制请求体大小。
func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(gatewayRequestBodyLimitKey, maxBytes)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// rejectInvalidGatewayBody 在基础鉴权后、复合和智能 Key 首次选组前检查同一份正文。
// @project-doc docs/domains/api_key_model_redirects.md#gateway_field_validation
func rejectInvalidGatewayBody(c *gin.Context) bool {
	req := c.Request
	if req.Body == nil || req.Body == http.NoBody {
		return false
	}
	switch req.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut:
	default:
		return false
	}
	err := httputil.PrepareGatewayRequestBody(req, c.GetInt64(gatewayRequestBodyLimitKey))
	if err == nil {
		return false
	}
	status, message := http.StatusBadRequest, "Failed to read request body"
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, jsonutil.ErrDuplicateField):
		MarkIngressRejected(c, IngressRejectConflictingRequestFields)
		message = "Request body contains conflicting fields"
	case errors.As(err, &tooLarge):
		status, message = http.StatusRequestEntityTooLarge, httputil.BodyTooLargeMessage(tooLarge.Limit)
	default:
		httputil.LogRequestBodyReadFailure(logger.FromContext(req.Context()), req, err)
	}
	path := req.URL.Path
	switch {
	case strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/antigravity/v1beta/"):
		// Google 错误体不含类型，运维分类需要单独登记。
		service.SetOpsLocalErrorType(c, "invalid_request_error", "")
		GoogleErrorWriter(c, status, message)
	case strings.Contains(path, "/messages"):
		c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "message": message}})
	default:
		c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
	}
	c.Abort()
	return true
}
