package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/typesafe"
	"github.com/stretchr/testify/require"
)

func TestContentModerationEngineProfilesPreserveLegacy(t *testing.T) {
	repo := &contentModerationTestSettingRepo{values: map[string]string{SettingKeyContentModerationConfig: `{"enabled":true,"mode":"pre_block","api_key":"old-openai-key","base_url":"https://openai.example","model":"omni","thresholds":{"sexual":0.65},"auto_ban_enabled":true}`}}
	s := &ContentModerationService{settingRepo: repo}
	view, err := s.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "openai", view.Engine)
	require.Equal(t, 1, view.APIKeyCount)
	require.Equal(t, ContentModerationDefaultThresholds(), view.EngineConfigs["typesafe"].Thresholds)
	engine := "typesafe"
	base := "https://typesafe.example"
	model := "jev-test"
	keys := []string{"new-typesafe-key"}
	thresholds := map[string]float64{"sexual": 0.92}
	view, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine, BaseURL: &base, Model: &model, APIKeys: &keys, Thresholds: &thresholds})
	require.NoError(t, err)
	require.Equal(t, base, view.BaseURL)
	require.Equal(t, 0.92, view.Thresholds["sexual"])
	require.True(t, view.AutoBanEnabled)
	require.Equal(t, "pre_block", view.Mode)
	var stored ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyContentModerationConfig]), &stored))
	require.Equal(t, "https://openai.example", stored.BaseURL)
	require.Equal(t, []string{"old-openai-key"}, stored.APIKeys)
	require.Equal(t, keys, stored.TypeSafe.APIKeys)
	serialized, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "new-typesafe-key")
	require.NotContains(t, string(serialized), "old-openai-key")
	engine = "openai"
	view, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine})
	require.NoError(t, err)
	require.Equal(t, "omni", view.Model)
	require.Equal(t, 0.65, view.Thresholds["sexual"])
	require.Equal(t, "jev-test", view.EngineConfigs["typesafe"].Model)
	require.Equal(t, 0.92, view.EngineConfigs["typesafe"].Thresholds["sexual"])
	engine = "typesafe"
	view, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine, EngineConfigs: map[string]UpdateContentModerationEngineInput{"typesafe": {ClearAPIKey: true}}})
	require.NoError(t, err)
	require.Zero(t, view.APIKeyCount)
	require.Equal(t, 1, view.EngineConfigs["openai"].APIKeyCount)
}

func TestContentModerationEngineThresholdDefaultsAreIndependent(t *testing.T) {
	openai := moderationEngineDefaults(ContentModerationEngineOpenAI)
	typeSafe := moderationEngineDefaults(ContentModerationEngineTypeSafe)
	require.Equal(t, openai.Thresholds, typeSafe.Thresholds)
	typeSafe.Thresholds["sexual"] = 0.8
	require.Equal(t, 0.65, openai.Thresholds["sexual"])
	require.Equal(t, openai.Thresholds, moderationEngineDefaults(ContentModerationEngineTypeSafe).Thresholds)
}

func TestContentModerationTypeSafeAllCategoriesAndImages(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/v1/systemone", r.URL.Path)
		var request typesafe.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "需要审核的文字", request.State)
		require.Len(t, request.Questions, 13)
		answers := map[string]any{}
		for _, category := range ContentModerationCategories() {
			q, ok := request.Questions[category]
			require.True(t, ok)
			require.NotEmpty(t, q.Instructions)
			require.Equal(t, "noul", q.Type)
			answers[category] = map[string]any{"type": "noul", "noul": 0.1}
		}
		answers["sexual"] = map[string]any{"type": "noul", "noul": 0.9}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixed", "answers": answers}))
	}))
	defer server.Close()
	cfg := defaultContentModerationConfig()
	cfg.Engine = "typesafe"
	cfg.BaseURL = server.URL
	cfg.Model = "jev-latest"
	cfg.APIKeys = []string{"test-key"}
	s := &ContentModerationService{httpClient: server.Client()}
	content := ContentModerationInput{Text: "需要审核的文字", Images: []string{"https://private.invalid/image.png"}}
	result, err := s.callModeration(context.Background(), cfg, content.ModerationInput())
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.Len(t, result.CategoryScores, 13)
	require.Equal(t, &ContentModerationEngineMeta{Engine: "typesafe", Model: "jev-fixed", RulesVersion: TypeSafeModerationRulesVersion, SkippedImages: 1}, result.EngineMeta)
	trial := buildContentModerationTestAuditResult(result, cfg.Thresholds)
	require.Equal(t, result.EngineMeta, trial.EngineMeta)
	_, err = s.callModeration(context.Background(), cfg, ContentModerationInput{Images: content.Images}.ModerationInput())
	require.ErrorContains(t, err, "no text to audit")
	require.Equal(t, int32(1), calls.Load())
}

func TestContentModerationTypeSafeFailureOpenAndKeyIsolation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(429) }))
	defer server.Close()
	repo := &contentModerationTestRepo{}
	s := &ContentModerationService{httpClient: server.Client(), repo: repo}
	cfg := defaultContentModerationConfig()
	cfg.Engine = "typesafe"
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"shared-key"}
	cfg.RecordNonHits = true
	decision := s.checkSync(context.Background(), ContentModerationCheckInput{UserID: 123, Model: "business-model", Provider: "business-provider"}, cfg, ContentModerationInput{Text: "normal text"}, "hash", nil, true)
	require.True(t, decision.Allowed)
	require.False(t, decision.Flagged)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "error", repo.logs[0].Action)
	require.False(t, repo.logs[0].Flagged)
	require.Equal(t, "business-model", repo.logs[0].Model)
	require.Equal(t, "business-provider", repo.logs[0].Provider)
	require.Equal(t, "typesafe", repo.logs[0].EngineMeta.Engine)
	require.Empty(t, repo.logs[0].EngineMeta.Model)
	require.True(t, s.isAPIKeyFrozen("shared-key", time.Now(), "typesafe"))
	require.False(t, s.isAPIKeyFrozen("shared-key", time.Now(), "openai"))
	require.Equal(t, "frozen", s.apiKeyStatuses(cfg)[0].Status)
	require.Equal(t, "unknown", s.apiKeyStatuses(&ContentModerationConfig{Engine: "openai", APIKeys: cfg.APIKeys})[0].Status)
	require.Zero(t, s.preBlockAPIKeyAvailableCount(cfg))
}

func TestContentModerationTypeSafeTestDoesNotSwitchActiveEngine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer typesafe-key", r.Header.Get("Authorization"))
		require.Equal(t, "/v1/systemone", r.URL.Path)
		answers := map[string]any{}
		for _, k := range ContentModerationCategories() {
			answers[k] = map[string]any{"type": "noul", "noul": 0.4}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"model": "jev-test", "answers": answers}))
	}))
	defer server.Close()
	repo := &contentModerationTestSettingRepo{values: map[string]string{}}
	cfg := defaultContentModerationConfig()
	cfg.APIKeys = []string{"openai-key"}
	cfg.TypeSafe = moderationEngineDefaults("typesafe")
	cfg.TypeSafe.APIKeys = []string{"typesafe-key"}
	cfg.TypeSafe.BaseURL = server.URL
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo.values[SettingKeyContentModerationConfig] = string(raw)
	s := &ContentModerationService{settingRepo: repo, httpClient: server.Client()}
	thresholds := map[string]float64{"sexual": 0.3}
	result, err := s.TestAPIKeys(context.Background(), TestContentModerationAPIKeysInput{Engine: "typesafe", Prompt: "test", Thresholds: &thresholds})
	require.NoError(t, err)
	require.True(t, result.AuditResult.Flagged)
	require.Equal(t, "jev-test", result.AuditResult.EngineMeta.Model)
	require.Equal(t, string(raw), repo.values[SettingKeyContentModerationConfig])
}

func TestContentModerationEngineRuntimeSwitchKeepsInFlightSnapshot(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.APIKeys = []string{"openai-key"}
	cfg.TypeSafe = moderationEngineDefaults("typesafe")
	cfg.TypeSafe.APIKeys = []string{"typesafe-key"}
	cfg.TypeSafe.Thresholds["sexual"] = 0.91
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(raw),
	}}
	s := runtimeCacheTestService(repo, time.Hour)
	old, err := s.loadRuntimeSnapshot(context.Background())
	require.NoError(t, err)
	engine := "typesafe"
	_, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine})
	require.NoError(t, err)
	current, err := s.loadRuntimeSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "typesafe", current.config.Engine)
	require.Equal(t, []string{"typesafe-key"}, current.config.apiKeys())
	require.Equal(t, 0.91, current.config.Thresholds["sexual"])
	require.Equal(t, "openai", old.config.Engine)
	require.Equal(t, []string{"openai-key"}, old.config.apiKeys())
	status, err := s.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, "typesafe", status.Engine)
	require.Equal(t, scopedModerationKeyHash("typesafe-key", "typesafe"), status.APIKeyStatuses[0].KeyHash)
}

// 引擎切换不能改变既有 Key 权重、备注或按引擎隔离的身份哈希。
func TestContentModerationEngineSwitchPreservesKeyMetadata(t *testing.T) {
	repo := &contentModerationTestSettingRepo{values: map[string]string{}}
	s := &ContentModerationService{settingRepo: repo}
	openaiEntries := []ContentModerationAPIKeyEntryInput{{APIKey: "shared-key", Priority: 321, Note: "原有审核凭据"}}
	_, err := s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{APIKeyEntries: &openaiEntries})
	require.NoError(t, err)
	engine := "typesafe"
	typeEntries := []ContentModerationAPIKeyEntryInput{{APIKey: "shared-key", Priority: 777, Note: "独立风险凭据"}}
	_, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine, APIKeyEntries: &typeEntries})
	require.NoError(t, err)
	for _, selected := range []string{"openai", "typesafe", "openai", "typesafe"} {
		view, err := s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &selected})
		require.NoError(t, err)
		a, b := view.EngineConfigs["openai"].APIKeyStatuses, view.EngineConfigs["typesafe"].APIKeyStatuses
		require.Len(t, a, 1)
		require.Len(t, b, 1)
		require.Equal(t, 321, a[0].Priority)
		require.Equal(t, "原有审核凭据", a[0].Note)
		require.Equal(t, 777, b[0].Priority)
		require.Equal(t, "独立风险凭据", b[0].Note)
		require.NotEqual(t, a[0].KeyHash, b[0].KeyHash)
	}
}

// 不支持的媒体保留审核不完整事实，但不能冻结凭据，也不能违反无留存边界。
func TestContentModerationTypeSafeImagesKeepKeyHealthyAndNoMediaRetention(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answers := map[string]any{}
		for _, category := range ContentModerationCategories() {
			answers[category] = map[string]any{"type": "noul", "noul": 0.1}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"model": "jev-test", "answers": answers}))
	}))
	defer server.Close()
	repo := &contentModerationTestRepo{}
	s := &ContentModerationService{httpClient: server.Client(), repo: repo}
	cfg := defaultContentModerationConfig()
	cfg.Engine = "typesafe"
	cfg.BaseURL = server.URL
	cfg.APIKeys = []string{"healthy-key"}
	cfg.RecordNonHits = true
	content := ContentModerationInput{Text: "私密正文", Images: []string{"https://private.invalid/secret.png"}}
	content.Normalize()
	for range 4 {
		decision := s.checkSync(context.Background(), ContentModerationCheckInput{UserID: 123, NoMediaRetention: true}, cfg, content, "hash", nil, true)
		require.True(t, decision.Allowed)
	}
	require.Len(t, repo.logs, 4)
	for _, log := range repo.logs {
		require.False(t, log.AuditComplete)
		require.Equal(t, 1, log.FailedUnitCount)
		require.Equal(t, 1, log.EngineMeta.SkippedImages)
		require.Equal(t, "jev-test", log.EngineMeta.Model)
		require.Empty(t, log.InputExcerpt)
		require.Empty(t, log.InputItems)
		require.Empty(t, log.Media)
	}
	status := s.apiKeyStatuses(cfg)[0]
	require.Equal(t, "ok", status.Status)
	require.Zero(t, status.FailureCount)
	require.False(t, s.isAPIKeyFrozen("healthy-key", time.Now(), "typesafe"))
}
