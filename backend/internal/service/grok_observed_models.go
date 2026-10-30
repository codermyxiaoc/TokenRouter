package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/xai"
)

const (
	grokObservedModelsExtraKey = "grok_observed_models"
	grokObservedModelsTTL      = 6 * time.Hour
	grokObservedModelsTimeout  = 15 * time.Second
)

type grokObservedModelsSnapshot struct {
	Models    []string `json:"models"`
	FetchedAt string   `json:"fetched_at"`
	Source    string   `json:"source,omitempty"`
	// Scope 记录目录来源；切换 base URL 或账号类型后旧快照不再视为新鲜。
	Scope *GrokCatalogScope `json:"scope,omitempty"`
}

var grokObservedModelsFlight sync.Map // accountID -> 进行中的同步标记

// scheduleGrokObservedModelsSync 尽力拉取 Grok OAuth 账号的上游 /v1/models 并把 ID 保存到 Extra；
// 调用方应在授权或探测成功后异步触发，不能长时间阻塞请求路径。
func (s *GrokQuotaService) scheduleGrokObservedModelsSync(account *Account) {
	if s == nil || account == nil || !account.IsGrokOAuth() || s.accountRepo == nil {
		return
	}
	id := account.ID
	if _, loaded := grokObservedModelsFlight.LoadOrStore(id, struct{}{}); loaded {
		return
	}
	// 复制账号快照供后台任务使用。
	acc := *account
	go func() {
		defer grokObservedModelsFlight.Delete(id)
		ctx, cancel := context.WithTimeout(context.Background(), grokObservedModelsTimeout)
		defer cancel()
		if err := s.syncGrokObservedModels(ctx, &acc); err != nil {
			// 只记录失败分类与状态码，不输出底层错误，避免传输层细节或凭据进入日志。
			kind, statusCode := grokObservedModelsSyncLogFields(err)
			slog.Debug("grok_observed_models_sync_failed", "account_id", id, "error_kind", kind, "status_code", statusCode)
		}
	}()
}

func (s *GrokQuotaService) syncGrokObservedModels(ctx context.Context, account *Account) error {
	if s == nil || account == nil {
		return nil
	}
	token := strings.TrimSpace(account.GetGrokAccessToken())
	if token == "" && s.tokenProvider != nil {
		// 尽力预热 token，避免强制刷新风暴。
		if at, err := s.tokenProvider.GetAccessToken(ctx, account); err == nil {
			token = strings.TrimSpace(at)
		}
	}
	if token == "" {
		return &grokCatalogSyncError{Kind: "configuration"}
	}
	baseURL := strings.TrimSpace(account.GetGrokBaseURL())
	if s.settingService != nil {
		baseURL = strings.TrimSpace(s.settingService.ResolveGrokBaseURL(ctx, account))
	}
	if baseURL == "" {
		baseURL = xai.DefaultCLIBaseURL
	}
	validator, err := grokBaseURLValidator(account, s.cfg)
	if err != nil {
		return &grokCatalogSyncError{Kind: "configuration", Err: err}
	}
	validatedBaseURL, err := validator(baseURL)
	if err != nil {
		return &grokCatalogSyncError{Kind: "configuration", Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildOpenAIModelsURL(validatedBaseURL), nil)
	if err != nil {
		return &grokCatalogSyncError{Kind: "configuration"}
	}
	// 同一来源的快照仍新鲜时跳过；来源变化、时间非法或在未来时重新拉取。
	scope, err := NewGrokCatalogScope(account.ID, account.Type, req.URL.String())
	if err != nil {
		return &grokCatalogSyncError{Kind: "configuration"}
	}
	if grokObservedSnapshotFresh(parseGrokObservedModels(account.Extra), scope, time.Now().UTC()) {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultGrokUpstreamUserAgent())
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(req.URL.String()) {
		applyGrokCLIHeaders(req.Header)
		if userID := strings.TrimSpace(account.GetCredential("sub")); userID != "" {
			req.Header.Set("X-UserID", userID)
		}
		if email := strings.TrimSpace(account.GetCredential("email")); email != "" {
			req.Header.Set("X-Email", email)
		}
	}
	account.ApplyHeaderOverrides(req.Header)
	if account.IsGrokOAuth() {
		// CLI 身份头必须在账号覆写之后写入，避免配置意外破坏上游协议身份。
		xai.ApplyCLIProxyHeaders(req)
	}

	proxyURL := ""
	if s.proxyRepo != nil && account.ProxyID != nil {
		if p, err := s.proxyRepo.GetByID(ctx, *account.ProxyID); err == nil && p != nil {
			proxyURL = p.URL()
		}
	}
	if s.httpUpstream == nil {
		return nil
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return &grokCatalogSyncError{Kind: "transport"}
	}
	defer func() { _ = resp.Body.Close() }()
	// 读取上限与模型列表接口共用配置，超出即放弃，不截断后解析。
	limit := resolveModelsListReadLimit(s.cfg)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return &grokCatalogSyncError{Kind: "transport"}
	}
	if int64(len(body)) > limit {
		return &grokCatalogSyncError{Kind: "too_large", StatusCode: resp.StatusCode}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &grokCatalogSyncError{Kind: "http_error", StatusCode: resp.StatusCode}
	}
	ids, err := extractGrokModelIDsFromModelsBody(body)
	if err != nil {
		kind := "invalid_response"
		if errors.Is(err, errGrokCatalogEmpty) {
			kind = "empty_catalog"
		}
		return &grokCatalogSyncError{Kind: kind, StatusCode: resp.StatusCode}
	}
	snap := grokObservedModelsSnapshot{
		Models:    ids,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Source:    "upstream_v1_models",
		Scope:     scope,
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		return err
	}
	return s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		grokObservedModelsExtraKey: asMap,
	})
}

// extractGrokModelIDsFromModelsBody 使用严格解析，错误响应与空目录都返回错误，不写入快照。
func extractGrokModelIDsFromModelsBody(body []byte) ([]string, error) {
	return parseGrokCatalogModelIDs(body)
}

func parseGrokObservedModels(extra map[string]any) *grokObservedModelsSnapshot {
	if extra == nil {
		return nil
	}
	raw, ok := extra[grokObservedModelsExtraKey]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snap grokObservedModelsSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil
	}
	if len(snap.Models) == 0 {
		return nil
	}
	return &snap
}
