package service

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

const openCodeSharedUsageTTL = 30 * time.Second
const openCodeSharedUsageMaxEntries = 256

type openCodeSharedUsageEntry struct {
	result    *UpstreamUsageQueryResult
	expiresAt time.Time
}

// openCodeGoSharedUsageKey 只合并官方 GO 的完整上游身份；中继与 Zen 不进入共享缓存。
// 保留认证头、代理、TLS、模式及查询配置，只移除本地账号 ID，并归一官方协议根路径。
func openCodeGoSharedUsageKey(account *Account, queryConfig UpstreamUsageQueryConfig) (string, bool) {
	if account == nil || account.Type != AccountTypeAPIKey || !account.IsOpenCodeGoPlan() || queryConfig.Adapter != UpstreamUsageAdapterOpenCodeGo {
		return "", false
	}
	base := strings.TrimSpace(queryConfig.BaseURL)
	if base == "" {
		base = upstreamUsageAccountBaseURL(account)
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "opencode.ai") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path != "/zen/go" && path != "/zen/go/v1" {
		return "", false
	}
	// 查询地址可独立配置，账号本身为中继时不能借官方查询配置进入共享身份。
	accountBase, err := url.Parse(upstreamUsageAccountBaseURL(account))
	if err != nil || accountBase.Scheme != "https" || !strings.EqualFold(accountBase.Host, "opencode.ai") || accountBase.User != nil || accountBase.RawQuery != "" || accountBase.Fragment != "" {
		return "", false
	}
	accountPath := strings.TrimRight(accountBase.Path, "/")
	if accountPath != "/zen/go" && accountPath != "/zen/go/v1" {
		return "", false
	}
	copy := *account
	copy.ID = 0
	copy.Credentials = make(map[string]any, len(account.Credentials)+1)
	for key, value := range account.Credentials {
		copy.Credentials[key] = value
	}
	copy.Credentials["base_url"] = DefaultOpenCodeGoBaseURL
	queryConfig.BaseURL = DefaultOpenCodeGoBaseURL
	return upstreamUsageContextFingerprint(&copy, queryConfig), true
}

func (s *UpstreamUsageService) loadOpenCodeSharedUsage(key string) *UpstreamUsageQueryResult {
	s.openCodeSharedMu.Lock()
	defer s.openCodeSharedMu.Unlock()
	entry, ok := s.openCodeSharedResults[key]
	if !ok {
		return nil
	}
	if !time.Now().Before(entry.expiresAt) {
		delete(s.openCodeSharedResults, key)
		return nil
	}
	return entry.result
}

func (s *UpstreamUsageService) storeOpenCodeSharedUsage(key string, result *UpstreamUsageQueryResult) {
	if result == nil {
		return
	}
	s.openCodeSharedMu.Lock()
	defer s.openCodeSharedMu.Unlock()
	if s.openCodeSharedResults == nil {
		s.openCodeSharedResults = make(map[string]openCodeSharedUsageEntry)
	}
	now := time.Now()
	for key, entry := range s.openCodeSharedResults {
		if !now.Before(entry.expiresAt) {
			delete(s.openCodeSharedResults, key)
		}
	}
	// 缓存有界且仅存短期成功观测；失败始终保留既有重试和监控逻辑。
	if len(s.openCodeSharedResults) >= openCodeSharedUsageMaxEntries {
		for oldest := range s.openCodeSharedResults {
			delete(s.openCodeSharedResults, oldest)
			break
		}
	}
	s.openCodeSharedResults[key] = openCodeSharedUsageEntry{result: result, expiresAt: now.Add(openCodeSharedUsageTTL)}
}

// cloneOpenCodeGoUsageResult 防止调用方改写共享结果，且观测时间保持真实上游查询时间。
func cloneOpenCodeGoUsageResult(result *UpstreamUsageQueryResult, accountID int64) *UpstreamUsageQueryResult {
	var copy UpstreamUsageQueryResult
	if data, err := json.Marshal(result); err == nil {
		_ = json.Unmarshal(data, &copy)
	}
	if result.Usage != nil {
		if data, err := json.Marshal(result.Usage); err == nil {
			copy.Usage = new(UpstreamUsageInfo)
			_ = json.Unmarshal(data, copy.Usage)
		}
	}
	copy.AccountID = accountID
	return &copy
}
