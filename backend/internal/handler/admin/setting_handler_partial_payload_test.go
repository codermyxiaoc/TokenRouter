//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 设置保存接口采用整份文档 PUT 语义，但客户端只发送关心的字段时不能重置其余设置。
// 例如仅发送 `{"risk_control_enabled":true}` 曾会清空 site_name，随后
// getStringOrDefault 会把空值渲染成内置默认值，导致登录页名称被静默修改。

func TestUpdateSettingsPartialPayloadKeepsUnsentKeys(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySiteName:         "Example Gateway",
		service.SettingKeySiteSubtitle:     "Example Gateway Platform",
		service.SettingKeySMTPHost:         "smtp.example.com",
		service.SettingKeySMTPFrom:         "noreply@example.com",
		service.SettingKeyTurnstileEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "true", repo.values[service.SettingKeyRiskControlEnabled],
		"调用方明确发送的字段必须写入")

	require.Equal(t, "Example Gateway", repo.values[service.SettingKeySiteName])
	require.Equal(t, "Example Gateway Platform", repo.values[service.SettingKeySiteSubtitle])
	require.Equal(t, "smtp.example.com", repo.values[service.SettingKeySMTPHost])
	require.Equal(t, "noreply@example.com", repo.values[service.SettingKeySMTPFrom])
	require.Equal(t, "true", repo.values[service.SettingKeyTurnstileEnabled])
}

// 完整载荷仍保留整份文档语义：明确发送的零值字段仍应被清空。
func TestUpdateSettingsFullPayloadStillClearsSentEmptyFields(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySiteName: "Example Gateway",
	})

	rec := doUpdateSettings(t, h, map[string]any{"site_name": ""}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "", repo.values[service.SettingKeySiteName],
		"明确发送的空值表示主动清空，而不是省略字段")
}

// smtp_from_email 是唯一一个 JSON 名称与持久化设置键不同的请求字段，
// 别名映射可避免它被误判为始终未发送。
func TestUpdateSettingsSMTPFromAliasIsWritable(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySMTPFrom: "old@example.com",
	})

	rec := doUpdateSettings(t, h, map[string]any{"smtp_from_email": "new@example.com"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "new@example.com", repo.values[service.SettingKeySMTPFrom])
}

// 创作台开关与 team 同款部分更新语义：显式发送时写入，省略时保留存储值。
func TestUpdateSettingsCreativeEnabledPartialSemantics(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyCreativeEnabled: "true",
	})

	rec := doUpdateSettings(t, h, map[string]any{"creative_enabled": false}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo.values[service.SettingKeyCreativeEnabled])

	h2, repo2 := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyCreativeEnabled: "false",
	})
	rec = doUpdateSettings(t, h2, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "false", repo2.values[service.SettingKeyCreativeEnabled],
		"未发送 creative_enabled 时必须保留存储值")
}

func TestUpdateSettingsCreativeModelSettingsPartialSemantics(t *testing.T) {
	stored := `[{"group_id":12,"model":"gpt-image-2","operations":["generate"]}]`
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyCreativeModelSettings: stored,
	})

	// 省略字段时保持现有白名单，不因整份设置表单的其它字段而清空。
	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, stored, repo.values[service.SettingKeyCreativeModelSettings])

	// 显式空数组表示管理员主动关闭全部生图模型。
	rec = doUpdateSettings(t, h, map[string]any{"creative_model_settings": []any{}}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, "[]", repo.values[service.SettingKeyCreativeModelSettings])

	// 非法能力在保存前拒绝，原值不被覆盖。
	rec = doUpdateSettings(t, h, map[string]any{
		"creative_model_settings": []map[string]any{{
			"group_id":   12,
			"model":      "gpt-image-2",
			"operations": []string{"upscale"},
		}},
	}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, "[]", repo.values[service.SettingKeyCreativeModelSettings])
}

// TestUpdateSettingsCreativeWorkerCountPartialSemantics 验证 worker 数量可部分更新且保留旧值。
func TestUpdateSettingsCreativeWorkerCountPartialSemantics(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyCreativeWorkerCount: "4",
	})

	rec := doUpdateSettings(t, h, map[string]any{"creative_worker_count": 7}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "7", repo.values[service.SettingKeyCreativeWorkerCount])

	rec = doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "7", repo.values[service.SettingKeyCreativeWorkerCount])
}

// TestUpdateSettingsRejectsInvalidCreativeWorkerCount 验证 worker 数量必须为正整数。
func TestUpdateSettingsRejectsInvalidCreativeWorkerCount(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyCreativeWorkerCount: "4",
	})

	for _, value := range []int{0, -1} {
		rec := doUpdateSettings(t, h, map[string]any{"creative_worker_count": value}, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "4", repo.values[service.SettingKeyCreativeWorkerCount])
	}
}

// TestUpdateSettingsNormalizesGeminiInpaintBeforeSave 校验管理员保存会清理旧 Gemini inpaint。
func TestUpdateSettingsNormalizesGeminiInpaintBeforeSave(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	h.SetCreativeModelReader(&creativeModelCandidateReaderStub{sanitizeGemini: true})

	rec := doUpdateSettings(t, h, map[string]any{
		"creative_model_settings": []map[string]any{{
			"group_id":   12,
			"model":      "gemini-3.1-flash-image",
			"operations": []string{"generate", "inpaint"},
		}},
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `[{"group_id":12,"model":"gemini-3.1-flash-image","operations":["generate"]}]`, repo.values[service.SettingKeyCreativeModelSettings])
}

type creativeModelCandidateReaderStub struct {
	candidates     []service.CreativeModelCandidate
	sanitizeGemini bool
}

func (s *creativeModelCandidateReaderStub) ListCreativeModelCandidates(context.Context) ([]service.CreativeModelCandidate, error) {
	return s.candidates, nil
}

func (s *creativeModelCandidateReaderStub) NormalizeCreativeModelSettingsForSave(_ context.Context, input []service.CreativeModelSetting) ([]service.CreativeModelSetting, error) {
	if !s.sanitizeGemini {
		return input, nil
	}
	normalized, err := service.NormalizeCreativeModelSettings(input)
	if err != nil {
		return nil, err
	}
	for i := range normalized {
		if normalized[i].GroupID != 12 {
			continue
		}
		filtered := normalized[i].Operations[:0]
		for _, operation := range normalized[i].Operations {
			if operation != service.CreativeOperationInpaint {
				filtered = append(filtered, operation)
			}
		}
		normalized[i].Operations = filtered
	}
	return normalized, nil
}

func TestListCreativeModelCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewSettingHandler(service.NewSettingService(&settingHandlerRepoStub{values: map[string]string{}}, &config.Config{}), nil, nil, nil, nil, nil, nil)
	h.SetCreativeModelReader(&creativeModelCandidateReaderStub{candidates: []service.CreativeModelCandidate{{
		GroupID:    12,
		GroupName:  "Exclusive Images",
		Platform:   "grok",
		Model:      "grok-imagine",
		Operations: []string{service.CreativeOperationGenerate},
	}}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/creative-model-candidates", nil)
	h.ListCreativeModelCandidates(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Exclusive Images")
	require.Contains(t, rec.Body.String(), "grok-imagine")
}

// TestGetCreativeWorkerStatus 验证创作台 worker 状态接口返回回调快照，未注入回调时返回未运行零值。
func TestGetCreativeWorkerStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := service.NewSettingService(&settingHandlerRepoStub{values: map[string]string{}}, &config.Config{})
	h := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/creative-worker-status", nil)
	h.GetCreativeWorkerStatus(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"running":false`)
	require.Contains(t, rec.Body.String(), `"worker_count":0`)
	require.Contains(t, rec.Body.String(), `"busy_workers":0`)

	svc.SetCreativeWorkerStatusCallback(func() service.CreativeWorkerStatus {
		return service.CreativeWorkerStatus{Running: true, WorkerCount: 128, BusyWorkers: 60}
	})
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/creative-worker-status", nil)
	h.GetCreativeWorkerStatus(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"running":true`)
	require.Contains(t, rec.Body.String(), `"worker_count":128`)
	require.Contains(t, rec.Body.String(), `"busy_workers":60`)
}

func TestUpdateSettingsGrokDefaultBaseURLModeIsWritable(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyGrokDefaultBaseURLMode: service.GrokDefaultBaseURLModeCLI,
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"grok_default_base_url_mode": service.GrokDefaultBaseURLModeEUWest1,
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.GrokDefaultBaseURLModeEUWest1, repo.values[service.SettingKeyGrokDefaultBaseURLMode])
}

func TestUpdateSettingsRejectsTwoCaptchaProviders(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTurnstileEnabled:   "true",
		service.SettingKeyTurnstileSiteKey:   "site-key",
		service.SettingKeyTurnstileSecretKey: "turnstile-secret",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"turnstile_enabled":                true,
		"turnstile_site_key":               "site-key",
		"turnstile_secret_key":             "turnstile-secret",
		"tencent_captcha_enabled":          true,
		"tencent_captcha_app_id":           "123456789",
		"tencent_captcha_app_secret_key":   "app-secret",
		"tencent_captcha_cloud_secret_id":  "cloud-secret-id",
		"tencent_captcha_cloud_secret_key": "cloud-secret-key",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "cannot be enabled at the same time")
}

func TestUpdateSettingsRequiresFourTencentCaptchaCredentialsWhenEnabled(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_enabled": true,
		"tencent_captcha_app_id":  "123456789",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "AppSecretKey")
}

func TestUpdateSettingsRetainsStoredTencentCaptchaCredentialsWhenInputsEmpty(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaAppSecretKey:   "stored-app-secret",
		service.SettingKeyTencentCaptchaCloudSecretID:  "stored-cloud-secret-id",
		service.SettingKeyTencentCaptchaCloudSecretKey: "stored-cloud-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_enabled":          true,
		"tencent_captcha_app_id":           "123456789",
		"tencent_captcha_app_secret_key":   "",
		"tencent_captcha_cloud_secret_id":  "",
		"tencent_captcha_cloud_secret_key": "",
	}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "stored-app-secret", repo.values[service.SettingKeyTencentCaptchaAppSecretKey])
	require.Equal(t, "stored-cloud-secret-id", repo.values[service.SettingKeyTencentCaptchaCloudSecretID])
	require.Equal(t, "stored-cloud-secret-key", repo.values[service.SettingKeyTencentCaptchaCloudSecretKey])
}

// 天御站点决定前端加载哪个 SDK 与服务端打哪个接入点，两端必须一致。
// 部分载荷把它重置回中国站，会让已配国际站的部署在下一次任意保存后整体失效。
func TestUpdateSettingsPartialPayloadKeepsTencentCaptchaRegion(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaRegion: service.TencentCaptchaRegionINTL,
	})

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.TencentCaptchaRegionINTL,
		repo.values[service.SettingKeyTencentCaptchaRegion])
}

func TestUpdateSettingsNormalizesUnknownTencentCaptchaRegion(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaRegion: service.TencentCaptchaRegionINTL,
	})

	rec := doUpdateSettings(t, h, map[string]any{"tencent_captcha_region": "sgp"}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.TencentCaptchaRegionCN,
		repo.values[service.SettingKeyTencentCaptchaRegion],
		"未知站点必须落回中国站，不能写入无法识别的值")
}

func TestUpdateSettingsWritesTencentCaptchaRegionWhenSent(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"tencent_captcha_region": "intl"}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, service.TencentCaptchaRegionINTL,
		repo.values[service.SettingKeyTencentCaptchaRegion])
}

func TestUpdateSettingsValidatesTencentCaptchaAppIDWhenEnabledFlagIsOmitted(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaEnabled:        "true",
		service.SettingKeyTencentCaptchaAppID:          "123456789",
		service.SettingKeyTencentCaptchaAppSecretKey:   "stored-app-secret",
		service.SettingKeyTencentCaptchaCloudSecretID:  "stored-cloud-secret-id",
		service.SettingKeyTencentCaptchaCloudSecretKey: "stored-cloud-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_app_id": "not-a-number",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "positive integer")
}
