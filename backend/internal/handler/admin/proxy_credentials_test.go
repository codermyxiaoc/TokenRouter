package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProxyUpdateCredentialJSONPresence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body string
		present    bool
		value      string
	}{
		{"omitted", `{}`, false, ""},
		{"null", `{"username":null,"password":null}`, false, ""},
		{"empty", `{"username":"","password":""}`, true, ""},
		{"value", `{"username":"new","password":"new"}`, true, "new"},
		{"spaces", `{"username":" ","password":" "}`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newStubAdminService()
			r := gin.New()
			r.PUT("/proxies/:id", NewProxyHandler(svc).Update)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/proxies/9", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			require.Equal(t, 200, w.Code)
			require.Len(t, svc.updatedProxies, 1)
			input := svc.updatedProxies[0]
			if tc.present {
				require.NotNil(t, input.Username)
				require.NotNil(t, input.Password)
				require.Equal(t, tc.value, *input.Username)
				require.Equal(t, tc.value, *input.Password)
			} else {
				require.Nil(t, input.Username)
				require.Nil(t, input.Password)
			}
		})
	}
}
