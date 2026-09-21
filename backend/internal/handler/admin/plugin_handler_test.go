package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pluginConfigHandlerRepository struct {
	service.PluginRepository
	config string
	err    error
	gets   int
}

func (r *pluginConfigHandlerRepository) GetByID(context.Context, int64) (*service.PluginInstallation, error) {
	r.gets++
	if r.err != nil {
		return nil, r.err
	}
	return &service.PluginInstallation{ID: 7, ConfigEncrypted: r.config}, nil
}

type pluginHandlerTestEncryptor struct{}

func (pluginHandlerTestEncryptor) Encrypt(value string) (string, error) { return value, nil }
func (pluginHandlerTestEncryptor) Decrypt(value string) (string, error) { return value, nil }

// 插件的合法 code/data 配置不应与宿主 API 响应封装发生冲突。
func TestPluginGetConfigPreservesEnvelopeLikeFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, config := range []string{`{"code":200,"region":"x"}`, `{"code":0,"data":{"enabled":true}}`} {
		manager := service.NewPluginManager(&pluginConfigHandlerRepository{config: config}, pluginHandlerTestEncryptor{}, nil, service.PluginHostInfo{}, nil)
		router := gin.New()
		router.GET("/plugins/:id/config", NewPluginHandler(manager).GetConfig)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/plugins/7/config", nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		var envelope struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
		require.Zero(t, envelope.Code)
		require.JSONEq(t, config, string(envelope.Data))
	}
}

// 缺失的合法插件 ID 是 404，真实存储故障仍是 500，非法 ID 在查库前被拒绝。
func TestPluginReadHandlersMissingInstallation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []struct {
		path string
		bind func(*PluginHandler) gin.HandlerFunc
	}{
		{"/plugins/:id", func(h *PluginHandler) gin.HandlerFunc { return h.Get }},
		{"/plugins/:id/status", func(h *PluginHandler) gin.HandlerFunc { return h.Status }},
		{"/plugins/:id/config", func(h *PluginHandler) gin.HandlerFunc { return h.GetConfig }},
		{"/plugins/:id/ui-session", func(h *PluginHandler) gin.HandlerFunc { return h.CreateUISession }},
	} {
		for _, tc := range []struct {
			name string
			id   string
			err  error
			want int
		}{
			{"missing", "999999", sql.ErrNoRows, http.StatusNotFound},
			{"wrapped_missing", "999999", fmt.Errorf("读取插件: %w", sql.ErrNoRows), http.StatusNotFound},
			{"database_failure", "999999", errors.New("database unavailable"), http.StatusInternalServerError},
			{"invalid_id", "0", nil, http.StatusBadRequest},
		} {
			t.Run(route.path+"/"+tc.name, func(t *testing.T) {
				repo := &pluginConfigHandlerRepository{err: tc.err}
				manager := service.NewPluginManager(repo, pluginHandlerTestEncryptor{}, nil, service.PluginHostInfo{}, nil)
				router := gin.New()
				router.GET(route.path, route.bind(NewPluginHandler(manager)))
				recorder := httptest.NewRecorder()
				path := strings.Replace(route.path, ":id", tc.id, 1)
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
				require.Equal(t, tc.want, recorder.Code, recorder.Body.String())
				if tc.want == http.StatusNotFound {
					require.Contains(t, recorder.Body.String(), "PLUGIN_NOT_FOUND")
					require.NotContains(t, recorder.Body.String(), "sql:")
				}
				if tc.want == http.StatusBadRequest {
					require.Zero(t, repo.gets)
				} else {
					require.Equal(t, 1, repo.gets)
				}
			})
		}
	}
}

// 已安装但未启动的插件有可读取的状态，不应被错误归类为不存在。
func TestPluginStatusInstalledButStoppedRemainsReadable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := service.NewPluginManager(&pluginConfigHandlerRepository{}, pluginHandlerTestEncryptor{}, nil, service.PluginHostInfo{}, nil)
	router := gin.New()
	router.GET("/plugins/:id/status", NewPluginHandler(manager).Status)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/plugins/7/status", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "插件未运行")
}
