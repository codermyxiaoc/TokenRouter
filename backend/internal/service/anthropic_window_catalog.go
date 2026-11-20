package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const anthropicLongContextThreshold = 900_000

// anthropicWindowRegistry 只缓存公共 Anthropic 型号的常规窗口，账号凭据和目录不进入缓存。
type anthropicWindowRegistry struct {
	mu      sync.Mutex
	updated time.Time
	windows map[string]int64
}

// anthropicCatalogBinding 防止修改 API Key、账号类型或目标站点后仍采用旧目录。
// OAuth 访问令牌定期轮换，绑定到稳定的账号与组织标识。
func anthropicCatalogBinding(account *Account) string {
	if account == nil || !account.IsAnthropic() {
		return ""
	}
	base := "https://api.anthropic.com"
	var identity string
	switch {
	case account.Type == AccountTypeAPIKey:
		base = getAnthropicGatewayBaseURL(account)
		identity = account.GetCredential("api_key")
	case account.IsOAuth():
		// 自定义转发端点与当前官方 OAuth 目录不是同一来源。
		if account.IsCustomBaseURLEnabled() {
			return ""
		}
		uuid := strings.TrimSpace(account.GetCredential("account_uuid"))
		if uuid == "" {
			uuid = strings.TrimSpace(account.GetExtraString("account_uuid"))
		}
		if uuid != "" {
			org := account.GetCredential("org_uuid")
			if org == "" {
				org = account.GetExtraString("org_uuid")
			}
			identity = uuid + "\x00" + org
		} else {
			// 老账号没有稳定身份时，按凭据代次绑定；重新授权后需要重新同步。
			identity = account.GetCredential("refresh_token")
			if identity == "" {
				identity = account.GetCredential("access_token")
			}
		}
	default:
		return ""
	}
	if identity == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(account.Type+"\x00"+strings.TrimRight(base, "/")+"\x00"+identity)))
}

// syncAnthropicModelCatalog 先确认账号可见的型号，再补充其常规上下文窗口。
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#anthropic_long_context
func (s *AccountTestService) syncAnthropicModelCatalog(ctx context.Context, account *Account) (*UpstreamModelCatalog, error) {
	if s.httpUpstream == nil {
		return nil, newUpstreamModelSyncConfigError("Upstream HTTP client is not configured", nil)
	}
	req, err := s.buildAnthropicUpstreamModelsRequest(ctx, account)
	if err != nil {
		return nil, err
	}
	body, err := s.readUpstreamModelsResponse(req, account)
	if err != nil {
		return nil, err
	}
	models, err := extractUpstreamModelIDs(body)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Upstream model list response was not valid JSON", err)
	}
	if len(models) == 0 {
		return nil, newUpstreamModelSyncUpstreamError("Upstream returned no supported models", nil)
	}
	catalog := &UpstreamModelCatalog{Models: models}
	binding := anthropicCatalogBinding(account)
	if binding == "" {
		return catalog, nil
	}
	windows := make(map[string]UpstreamModelMetadata)
	root := gjson.ParseBytes(body)
	lists := []gjson.Result{root.Get("data"), root.Get("models")}
	if root.IsArray() {
		lists = []gjson.Result{root}
	}
	for _, list := range lists {
		list.ForEach(func(_, entry gjson.Result) bool {
			// 复用 ID 接口的精确身份，不把厂商最大实验窗口当成该账号的常规窗口。
			id := upstreamModelEntryID(upstreamModelEntry{ID: entry.Get("id").String(), Slug: entry.Get("slug").String(), Name: entry.Get("name").String()})
			window := entry.Get("context_window").Int()
			if window <= 0 {
				window = entry.Get("limit.context").Int()
			}
			if maxInput := entry.Get("max_input_tokens").Int(); maxInput > 0 && (window <= 0 || maxInput < window) {
				window = maxInput
			}
			if id != "" && window > 0 {
				windows[id] = UpstreamModelMetadata{ContextWindow: window}
			}
			return true
		})
	}
	if len(windows) < len(models) && req.URL.Scheme == "https" && strings.EqualFold(req.URL.Host, "api.anthropic.com") && req.URL.Path == "/v1/models" {
		registry, regErr := s.fetchAnthropicWindowRegistry(ctx, account)
		if regErr != nil {
			catalog.Warnings = append(catalog.Warnings, "Anthropic context window catalog unavailable; only directly reported windows were used.")
		}
		for _, id := range models {
			if _, present := windows[id]; !present && registry[id] > 0 {
				windows[id] = UpstreamModelMetadata{ContextWindow: registry[id]}
			}
		}
	}
	catalog.Metadata = windows
	// 一次成功的目录刷新完整替换旧快照；已移除的模型或明确缩短的窗口随即失效。
	err = s.saveUpstreamMetadataSnapshot(ctx, account, UpstreamModelMetadataSnapshot{Source: "upstream", Binding: binding, UpdatedAt: time.Now().UTC(), Models: windows})
	return catalog, err
}

func (s *AccountTestService) fetchAnthropicWindowRegistry(ctx context.Context, account *Account) (map[string]int64, error) {
	cache := &s.anthropicRegistry
	cache.mu.Lock()
	if cache.windows != nil && time.Since(cache.updated) < 6*time.Hour {
		windows := cache.windows
		cache.mu.Unlock()
		return windows, nil
	}
	cache.mu.Unlock()
	// 单独构造公共请求，不复制账号的授权或自定义头；沿用代理、超时和响应大小上限。
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://models.dev/api.json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	body, err := s.readUpstreamModelsResponse(req, account)
	if err != nil {
		return nil, err
	}
	var registry struct {
		Anthropic struct {
			Models map[string]struct {
				Limit struct {
					Context int64 `json:"context"`
				} `json:"limit"`
			} `json:"models"`
		} `json:"anthropic"`
	}
	if err := json.Unmarshal(body, &registry); err != nil {
		return nil, err
	}
	windows := make(map[string]int64)
	for id, model := range registry.Anthropic.Models {
		if id != "" && model.Limit.Context > 0 {
			windows[id] = model.Limit.Context
		}
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("no Anthropic context windows in public registry")
	}
	cache.mu.Lock()
	cache.windows = windows
	cache.updated = time.Now()
	cache.mu.Unlock()
	return windows, nil
}
