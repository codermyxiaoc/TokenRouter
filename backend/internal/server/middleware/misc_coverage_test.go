//go:build unit

package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClientRequestID_GeneratesWhenMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(ClientRequestID())
	r.GET("/t", func(c *gin.Context) {
		v := c.Request.Context().Value(ctxkey.ClientRequestID)
		require.NotNil(t, v)
		id, ok := v.(string)
		require.True(t, ok)
		require.NotEmpty(t, id)
		require.Empty(t, c.Request.Header.Get(clientRequestIDHeader))
		require.Empty(t, c.Request.Header.Get(internalRequestIDHeader))
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set(internalRequestIDHeader, "spoofed-internal-request-id")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotEmpty(t, w.Header().Get("X-Client-Request-Id"))
	require.NotEqual(t, "spoofed-internal-request-id", w.Header().Get(internalRequestIDHeader))
}

func TestClientRequestIDSeparatesInternalAndParentIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var internalID string
	r := gin.New()
	r.Use(ClientRequestID())
	r.POST("/t", func(c *gin.Context) {
		id, ok := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		require.True(t, ok)
		require.NotEqual(t, "upstream-request-123", id)
		require.Len(t, id, 36)
		internalID = id
		parentID, ok := c.Request.Context().Value(ctxkey.ParentClientRequestID).(string)
		require.True(t, ok)
		require.Equal(t, "upstream-request-123", parentID)
		require.Equal(t, parentID, c.Request.Header.Get(clientRequestIDHeader))
		require.Equal(t, parentID, c.Writer.Header().Get(clientRequestIDHeader))
		require.Empty(t, c.Request.Header.Get(internalRequestIDHeader))
		require.Equal(t, id, c.Writer.Header().Get(internalRequestIDHeader))
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/t", nil)
	req.Header.Set(clientRequestIDHeader, "upstream-request-123")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "upstream-request-123", w.Header().Get(clientRequestIDHeader))
	require.Equal(t, internalID, w.Header().Get(internalRequestIDHeader))
}

func TestClientRequestIDRejectsUnsafeIncomingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(ClientRequestID())
	r.GET("/t", func(c *gin.Context) {
		id, ok := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		require.True(t, ok)
		require.Len(t, id, 36)
		_, parentOK := c.Request.Context().Value(ctxkey.ParentClientRequestID).(string)
		require.False(t, parentOK)
		require.Equal(t, "request id with spaces", c.Request.Header.Get(clientRequestIDHeader))
		require.Equal(t, id, c.Writer.Header().Get(clientRequestIDHeader))
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set(clientRequestIDHeader, "request id with spaces")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestClientRequestID_PreservesExisting(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(ClientRequestID())
	r.GET("/t", func(c *gin.Context) {
		id, ok := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		require.True(t, ok)
		require.Equal(t, "keep", id)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.ClientRequestID, "keep"))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "keep", w.Header().Get(clientRequestIDHeader))
}

func TestClientRequestID_ReplacesOversizedExistingID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(ClientRequestID())
	r.GET("/t", func(c *gin.Context) {
		id, ok := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		require.True(t, ok)
		require.Len(t, id, 36)
		c.String(http.StatusOK, id)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.ClientRequestID, strings.Repeat("x", 200)))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, w.Body.String(), w.Header().Get(clientRequestIDHeader))
}

func TestRequestBodyLimit_LimitsBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(RequestBodyLimit(4))
	r.POST("/t", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		require.Error(t, err)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/t", bytes.NewBufferString("12345"))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestForcePlatform_SetsContextAndGinValue(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(ForcePlatform("anthropic"))
	r.GET("/t", func(c *gin.Context) {
		require.True(t, HasForcePlatform(c))
		v, ok := GetForcePlatformFromContext(c)
		require.True(t, ok)
		require.Equal(t, "anthropic", v)

		ctxV := c.Request.Context().Value(ctxkey.ForcePlatform)
		require.Equal(t, "anthropic", ctxV)
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestAuthSubjectHelpers_RoundTrip(t *testing.T) {
	c := &gin.Context{}
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 1, Concurrency: 2})
	c.Set(string(ContextKeyUserRole), "admin")

	sub, ok := GetAuthSubjectFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(1), sub.UserID)
	require.Equal(t, 2, sub.Concurrency)

	role, ok := GetUserRoleFromContext(c)
	require.True(t, ok)
	require.Equal(t, "admin", role)
}

func TestAPIKeyAndSubscriptionFromContext(t *testing.T) {
	c := &gin.Context{}

	key := &service.APIKey{ID: 1}
	c.Set(string(ContextKeyAPIKey), key)
	gotKey, ok := GetAPIKeyFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(1), gotKey.ID)

	sub := &service.UserSubscription{ID: 2}
	c.Set(string(ContextKeySubscription), sub)
	gotSub, ok := GetSubscriptionFromContext(c)
	require.True(t, ok)
	require.Equal(t, int64(2), gotSub.ID)
}
