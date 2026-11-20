package service

import (
	"bytes"
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
	catalog, err := s.readAnthropicModelCatalog(req, account)
	if err != nil {
		return nil, err
	}
	binding := anthropicCatalogBinding(account)
	if binding == "" {
		catalog.Metadata = nil
		return catalog, nil
	}
	if len(catalog.Metadata) < len(catalog.Models) && req.URL.Scheme == "https" && strings.EqualFold(req.URL.Host, "api.anthropic.com") && req.URL.Path == "/v1/models" {
		registry, regErr := s.fetchAnthropicWindowRegistry(ctx, account)
		if regErr != nil {
			catalog.Warnings = append(catalog.Warnings, "Anthropic context window catalog unavailable; only directly reported windows were used.")
		}
		for _, id := range catalog.Models {
			if _, present := catalog.Metadata[id]; !present && registry[id] > 0 {
				catalog.Metadata[id] = UpstreamModelMetadata{ContextWindow: registry[id]}
			}
		}
	}
	// 全部页成功后只写一次；合法空目录同样覆盖旧快照，撤销最后一个型号的资格。
	if err := s.saveUpstreamMetadataSnapshot(ctx, account, UpstreamModelMetadataSnapshot{Source: "upstream", Binding: binding, UpdatedAt: time.Now().UTC(), Models: catalog.Metadata}); err != nil {
		return nil, err
	}
	return catalog, nil
}

// readAnthropicModelCatalog 为保存同步和只读预览收集完整目录，游标只用于原端点的查询参数。
func (s *AccountTestService) readAnthropicModelCatalog(req *http.Request, account *Account) (*UpstreamModelCatalog, error) {
	catalog := &UpstreamModelCatalog{Metadata: make(map[string]UpstreamModelMetadata)}
	seenCursors := make(map[string]bool)
	remaining := resolveModelsListReadLimit(s.cfg)
	for {
		body, err := s.readUpstreamModelsResponseWithLimit(req, account, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(body))
		next, err := appendAnthropicModelPage(catalog, body)
		if err != nil {
			return nil, newUpstreamModelSyncUpstreamError("Invalid upstream model list page", err)
		}
		if next == "" {
			catalog.Models = dedupeAndSortModelIDs(catalog.Models)
			return catalog, nil
		}
		if seenCursors[next] {
			return nil, newUpstreamModelSyncUpstreamError("Upstream model list cursor repeated", nil)
		}
		seenCursors[next] = true
		req = req.Clone(req.Context())
		query := req.URL.Query()
		query.Set("after_id", next)
		req.URL.RawQuery = query.Encode()
	}
}

// appendAnthropicModelPage 兼容 data、models 和裸数组；只有明确的空数组可以作为空目录。
func appendAnthropicModelPage(catalog *UpstreamModelCatalog, body []byte) (string, error) {
	type entry struct {
		upstreamModelEntry
		ContextWindow  json.RawMessage `json:"context_window"`
		MaxInputTokens json.RawMessage `json:"max_input_tokens"`
		Limit          json.RawMessage `json:"limit"`
	}
	var page struct {
		Data    []entry         `json:"data"`
		Models  []entry         `json:"models"`
		HasMore json.RawMessage `json:"has_more"`
		LastID  json.RawMessage `json:"last_id"`
	}
	body = bytes.TrimLeft(body, " \t\r\n")
	var dst any = &page
	if len(body) > 0 && body[0] == '[' {
		dst = &page.Data
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return "", err
	}
	if page.Data == nil && page.Models == nil {
		return "", fmt.Errorf("missing model list")
	}
	before := len(catalog.Models)
	for _, list := range [][]entry{page.Data, page.Models} {
		for _, model := range list {
			id := upstreamModelEntryID(model.upstreamModelEntry)
			if id == "" {
				continue
			}
			catalog.Models = append(catalog.Models, id)
			// 只采信常规窗口。同一型号重复出现时采用已声明窗口中较小的值。
			window := gjson.ParseBytes(model.ContextWindow).Int()
			if window <= 0 {
				window = gjson.GetBytes(model.Limit, "context").Int()
			}
			if input := gjson.ParseBytes(model.MaxInputTokens).Int(); input > 0 && (window <= 0 || input < window) {
				window = input
			}
			prior := catalog.Metadata[id].ContextWindow
			if window > 0 && (prior == 0 || window < prior) {
				catalog.Metadata[id] = UpstreamModelMetadata{ContextWindow: window}
			}
		}
	}
	if len(page.Data)+len(page.Models) > 0 && len(catalog.Models) == before {
		return "", fmt.Errorf("model list contains no usable IDs")
	}
	switch string(page.HasMore) {
	case "", "false":
		return "", nil
	case "true":
		var cursor string
		if json.Unmarshal(page.LastID, &cursor) == nil && strings.TrimSpace(cursor) != "" {
			return cursor, nil
		}
	}
	return "", fmt.Errorf("invalid model list pagination")
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
