package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 经过真实服务校验再进入仓储替身，验证拒绝的HTTP请求不会触达资金写入。
type dailyActivityIngressRepo struct {
	calls   int
	uid     int64
	config  service.DailyActivityConfig
	failure error
}

func (r *dailyActivityIngressRepo) GetConfig(context.Context) (*service.DailyActivityConfig, error) {
	r.calls++
	c := service.DefaultDailyActivityConfig()
	return &c, r.failure
}
func (r *dailyActivityIngressRepo) SaveConfig(_ context.Context, c service.DailyActivityConfig) (*service.DailyActivityConfig, error) {
	r.calls++
	r.config = c
	return &c, r.failure
}
func (r *dailyActivityIngressRepo) Status(_ context.Context, uid int64) (*service.DailyActivityStatus, error) {
	r.calls++
	r.uid = uid
	return &service.DailyActivityStatus{}, r.failure
}
func (r *dailyActivityIngressRepo) Claim(_ context.Context, uid int64, _ service.DailyActivityClaimInput) (*service.DailyActivityClaimResult, error) {
	r.calls++
	r.uid = uid
	return &service.DailyActivityClaimResult{}, r.failure
}
func (r *dailyActivityIngressRepo) History(_ context.Context, uid int64, _ string, page, size int) (*service.DailyActivityHistory, error) {
	r.calls++
	r.uid = uid
	return &service.DailyActivityHistory{Items: []service.DailyActivityRecord{}, Page: page, PageSize: size}, r.failure
}
func dailyActivityIngressRouter(repo *dailyActivityIngressRepo, uid int64, role string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if role != "" {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: uid})
			c.Set(string(middleware.ContextKeyUserRole), role)
		}
	})
	h := NewDailyActivityHandler(service.NewDailyActivityService(repo, nil, nil))
	r.GET("/status", h.Status)
	r.GET("/history", h.History)
	r.POST("/claim", h.Claim)
	r.GET("/config", h.GetConfig)
	r.PUT("/config", h.SaveConfig)
	return r
}
func dailyActivityIngressRequest(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// 领取入参不能注入其他用户、金额、资格或SQL，也不能以异常JSON绕过服务校验。
func TestDailyActivityIngressMalformedClaims(t *testing.T) {
	valid := `{"activity_type":"recharge","all":false,"idempotency_key":"0123456789abcdef"}`
	cases := []struct{ name, body string }{
		{"空体", ""}, {"空对象", `{}`}, {"null", `null`}, {"数组", `[]`},
		{"标量", `true`}, {"不完整JSON", `{"activity_type":`},
		{"未知活动", strings.Replace(valid, `"recharge"`, `"invite"`, 1)},
		{"大小写活动", strings.Replace(valid, `"recharge"`, `"RECHARGE"`, 1)},
		{"活动前后空格", strings.Replace(valid, `"recharge"`, `" recharge "`, 1)},
		{"活动数值", strings.Replace(valid, `"recharge"`, `123`, 1)},
		{"活动null", strings.Replace(valid, `"recharge"`, `null`, 1)},
		{"批量字符串", strings.Replace(valid, `false`, `"true"`, 1)},
		{"批量数值", strings.Replace(valid, `false`, `1`, 1)},
		{"标识过短", strings.Replace(valid, `0123456789abcdef`, `123`, 1)},
		{"标识过长", strings.Replace(valid, `0123456789abcdef`, strings.Repeat("a", 101), 1)},
		{"标识空格", strings.Replace(valid, `0123456789abcdef`, `0123456789abc de`, 1)},
		{"标识换行", strings.Replace(valid, `0123456789abcdef`, `0123456789abcdef\n`, 1)},
		{"标识中文", strings.Replace(valid, `0123456789abcdef`, `中文0123456789abcdef`, 1)},
		{"标识注入", strings.Replace(valid, `0123456789abcdef`, `' OR 1=1; -- 123456789`, 1)},
		{"标识null", strings.Replace(valid, `"0123456789abcdef"`, `null`, 1)},
		{"标识对象", strings.Replace(valid, `"0123456789abcdef"`, `{}`, 1)},
		{"伪造金额", strings.Replace(valid, `"all":false`, `"amount":1000000,"all":false`, 1)},
		{"伪造用户", strings.Replace(valid, `"all":false`, `"user_id":999,"all":false`, 1)},
		{"伪造资格", strings.Replace(valid, `"all":false`, `"available_count":999,"all":false`, 1)},
		{"拼接对象", valid + `{}`}, {"拼接null", valid + `null`},
		{"越过8KB", valid + strings.Repeat(" ", 8192)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dailyActivityIngressRepo{}
			w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "user"), "POST", "/claim", tc.body)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			require.Zero(t, repo.calls)
		})
	}
	for _, key := range []string{strings.Repeat("a", 16), strings.Repeat("Z", 100), "a_B-0123456789abcdef"} {
		t.Run("合法标识长度"+fmt.Sprint(len(key)), func(t *testing.T) {
			repo := &dailyActivityIngressRepo{}
			w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "user"), "POST", "/claim?user_id=999", strings.Replace(valid, "0123456789abcdef", key, 1))
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, int64(7), repo.uid)
			require.Equal(t, 1, repo.calls)
		})
	}
}

// 配置通过真正服务校验，覆盖每个活动、上下界、精度、错误类型和嵌套未知字段。
func TestDailyActivityIngressConfigurationMatrix(t *testing.T) {
	config := service.DefaultDailyActivityConfig()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	for _, kind := range []string{"daily_gift", "recharge", "consumption"} {
		for _, field := range []string{"threshold", "reward_max"} {
			for _, value := range []any{0, -1, 0.001, 1000001, "1", nil} {
				t.Run(fmt.Sprintf("%s/%s/%v", kind, field, value), func(t *testing.T) {
					var body map[string]any
					require.NoError(t, json.Unmarshal(raw, &body))
					body[kind].(map[string]any)[field] = value
					input, err := json.Marshal(body)
					require.NoError(t, err)
					repo := &dailyActivityIngressRepo{}
					w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "admin"), "PUT", "/config", string(input))
					require.Equal(t, 400, w.Code, w.Body.String())
					require.Zero(t, repo.calls)
				})
			}
		}
		for _, value := range []string{`{"enabled":null,"threshold":10,"reward_max":1}`, `{"enabled":"false","threshold":10,"reward_max":1}`, `{"enabled":1,"threshold":10,"reward_max":1}`, `{"threshold":10,"reward_max":1,"amount":999}`, `null`, `{"threshold":1e309,"reward_max":1}`} {
			t.Run(kind+"/异常对象/"+value, func(t *testing.T) {
				var body map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(raw, &body))
				body[kind] = json.RawMessage(value)
				input, err := json.Marshal(body)
				require.NoError(t, err)
				repo := &dailyActivityIngressRepo{}
				w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "admin"), "PUT", "/config", string(input))
				require.Equal(t, 400, w.Code, w.Body.String())
				require.Zero(t, repo.calls)
			})
		}
	}
	// 八种单项组合必须原样传入仓储，不能把显式关闭误当成缺省启用。
	for bits := 0; bits < 8; bits++ {
		t.Run(fmt.Sprintf("合法开关%d", bits), func(t *testing.T) {
			c := config
			c.DailyGift.Enabled = bits&1 != 0
			c.Recharge.Enabled = bits&2 != 0
			c.Consumption.Enabled = bits&4 != 0
			c.DailyGift.Threshold = .01
			c.Recharge.RewardMax = 1000000
			c.Consumption.RewardMax = .10
			input, err := json.Marshal(c)
			require.NoError(t, err)
			repo := &dailyActivityIngressRepo{}
			w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "admin"), "PUT", "/config", string(input))
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, c, repo.config)
			require.Equal(t, 1, repo.calls)
		})
	}
}

// 查询必须使用会话身份，非法分页和类型不能触达仓储。
func TestDailyActivityIngressIdentityHistoryAndErrors(t *testing.T) {
	for _, path := range []string{"/status", "/history", "/config"} {
		for _, uid := range []int64{0, -1} {
			t.Run(fmt.Sprintf("invalid_actor_%s_%d", path, uid), func(t *testing.T) {
				repo := &dailyActivityIngressRepo{}
				w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, uid, "admin"), "GET", path, "")
				require.Equal(t, 401, w.Code)
				require.Zero(t, repo.calls)
			})
		}
	}
	for _, query := range []string{"page=0", "page=1.5", "page=NaN", "page=1000001", "page=18446744073709551615", "page_size=-1", "page_size=0", "page_size=101", "page_size=bad", "activity_type=invite", "activity_type=recharge%27%20OR%201%3D1"} {
		t.Run(query, func(t *testing.T) {
			repo := &dailyActivityIngressRepo{}
			w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "user"), "GET", "/history?"+query, "")
			require.Equal(t, 400, w.Code, w.Body.String())
			require.Zero(t, repo.calls)
		})
	}
	for _, path := range []string{"/status?user_id=999", "/history?user_id=999&page=1000000&page_size=100&activity_type=consumption"} {
		repo := &dailyActivityIngressRepo{}
		w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "admin"), "GET", path, "")
		require.Equal(t, 200, w.Code)
		require.Equal(t, int64(7), repo.uid)
	}
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"总停用", service.ErrDailyActivityDisabled, 403}, {"单项停用", service.ErrDailyActivityRuleDisabled, 403},
		{"无资格", service.ErrDailyActivityUnavailable, 409}, {"幂等冲突", service.ErrDailyActivityIdempotency, 409},
		{"数据库异常", errors.New("database_password_do_not_echo"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dailyActivityIngressRepo{failure: tc.err}
			w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "user"), "POST", "/claim", `{"activity_type":"recharge","idempotency_key":"0123456789abcdef"}`)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.NotContains(t, w.Body.String(), "database_password_do_not_echo")
		})
	}
}

// 模糊测试请求解码边界：任意输入只能得到受控响应，不能绕过会话身份或触达多次写入。
func FuzzDailyActivityClaimIngress(f *testing.F) {
	for _, seed := range []string{`{}`, `null`, `[]`, `{"activity_type":"recharge","idempotency_key":"0123456789abcdef"}`, `{"activity_type":"daily_gift","all":true,"idempotency_key":"0123456789abcdef"}`, `{"user_id":99,"amount":1000000}`, strings.Repeat("[", 200)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 16384 {
			t.Skip()
		}
		repo := &dailyActivityIngressRepo{}
		w := dailyActivityIngressRequest(dailyActivityIngressRouter(repo, 7, "user"), "POST", "/claim", body)
		require.Contains(t, []int{200, 400}, w.Code, w.Body.String())
		if w.Code == 200 {
			require.Equal(t, 1, repo.calls)
			require.Equal(t, int64(7), repo.uid)
		} else {
			require.Zero(t, repo.calls)
		}
	})
}
