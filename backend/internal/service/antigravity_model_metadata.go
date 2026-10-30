package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// UpstreamModelMetadata 只保存模型目录声明的能力，不参与模型授权、收费或参数改写。
type UpstreamModelMetadata struct {
	DisplayName              string   `json:"display_name,omitempty"`
	Reasoning                *bool    `json:"reasoning,omitempty"`
	SupportedReasoningLevels []string `json:"supported_reasoning_levels,omitempty"`
	DefaultReasoningLevel    string   `json:"default_reasoning_level,omitempty"`
	InputModalities          []string `json:"input_modalities,omitempty"`
	ContextWindow            int64    `json:"context_window,omitempty"`
	MaxOutputTokens          int64    `json:"max_output_tokens,omitempty"`
}

// UpstreamModelMetadataSnapshot 与账号其他extra键独立更新，禁止把原始上游正文落库。
type UpstreamModelMetadataSnapshot struct {
	Source    string                           `json:"source"`
	UpdatedAt time.Time                        `json:"updated_at"`
	Models    map[string]UpstreamModelMetadata `json:"models"`
}

// UpstreamModelCatalog 保留模型ID列表并附加可选能力；老调用方继续使用原列表接口。
type UpstreamModelCatalog struct {
	Models   []string                         `json:"models"`
	Metadata map[string]UpstreamModelMetadata `json:"metadata,omitempty"`
	Warnings []string                         `json:"warnings,omitempty"`
}

// GetUpstreamModelMetadataSnapshot 返回独立副本，避免读取者修改共享调度账号。

// UpstreamModelMetadataExtraKey 保存上游模型能力快照，只由模型同步写入；普通账号编辑不能覆盖或注入。
const UpstreamModelMetadataExtraKey = "upstream_model_metadata"

func (a *Account) GetUpstreamModelMetadataSnapshot() *UpstreamModelMetadataSnapshot {
	if a == nil || a.Extra == nil {
		return nil
	}
	raw, ok := a.Extra[UpstreamModelMetadataExtraKey]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot UpstreamModelMetadataSnapshot
	if json.Unmarshal(b, &snapshot) != nil || snapshot.Source != "upstream" {
		return nil
	}
	return &snapshot
}

// SyncUpstreamModelCatalog 只扩展Antigravity原生账号的管理员同步，不改变现有ID接口和其它平台。
func (s *AccountTestService) SyncUpstreamModelCatalog(ctx context.Context, account *Account) (*UpstreamModelCatalog, error) {
	if account == nil || s == nil {
		return nil, newUpstreamModelSyncConfigError("Account is required", nil)
	}
	if account.Platform != PlatformAntigravity || account.Type == AccountTypeAPIKey {
		models, err := s.FetchUpstreamSupportedModels(ctx, account)
		return &UpstreamModelCatalog{Models: models}, err
	}
	// 目录查询与原接口保持相同代理门禁，不能因能力扩展绕过管理员代理配置。
	if account.ProxyID != nil && (account.Proxy == nil || account.Proxy.ID != *account.ProxyID || !account.Proxy.IsActive() || account.Proxy.IsExpired(time.Now())) {
		return nil, newUpstreamModelSyncConfigError("Configured proxy is unavailable", nil)
	}
	models, body, err := s.fetchAntigravityOAuthUpstreamModels(ctx, account)
	if err != nil {
		return nil, err
	}
	metadata, err := extractAntigravityModelMetadata(body)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to read Antigravity model metadata", err)
	}
	result := &UpstreamModelCatalog{Models: models, Metadata: metadata}
	available := make(map[string]bool, len(models))
	for _, model := range models {
		available[model] = true
	}
	for _, model := range configuredUpstreamModelsForCapabilitySync(account) {
		if !available[model] {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Configured model %s is not in the selected upstream catalog", model))
		}
	}
	snapshot := UpstreamModelMetadataSnapshot{Source: "upstream", UpdatedAt: time.Now().UTC(), Models: metadata}
	encoded, _ := json.Marshal(snapshot)
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	if s.accountRepo != nil && account.ID > 0 {
		if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{UpstreamModelMetadataExtraKey: value}); err != nil {
			return nil, fmt.Errorf("save upstream model metadata: %w", err)
		}
	}
	// 仅替换调用方账号副本的extra，数据库使用按键更新，不覆盖其它配置。
	extra := make(map[string]any, len(account.Extra)+1)
	for key, v := range account.Extra {
		extra[key] = v
	}
	extra[UpstreamModelMetadataExtraKey] = value
	account.Extra = extra
	return result, nil
}

// configuredUpstreamModelsForCapabilitySync 排除运行时补齐项，保留管理员明确配置的模型目标。
func configuredUpstreamModelsForCapabilitySync(account *Account) []string {
	if account == nil {
		return nil
	}
	models := make([]string, 0)
	for requested, mapped := range account.GetModelMapping() {
		if account.isAntigravityInjectedDefaultPassthrough(requested, mapped) {
			continue
		}
		mapped = strings.TrimSpace(mapped)
		if mapped != "" && !strings.Contains(mapped, "*") {
			models = append(models, mapped)
		}
	}
	return dedupeAndSortModelIDs(models)
}

// extractAntigravityModelMetadata 仅采用端点直接声明，不从其它端点或定价猜测账号能力。
func extractAntigravityModelMetadata(body []byte) (map[string]UpstreamModelMetadata, error) {
	var catalog struct {
		Models map[string]struct {
			DisplayName      string `json:"displayName"`
			SupportsImages   *bool  `json:"supportsImages"`
			SupportsThinking *bool  `json:"supportsThinking"`
			ThinkingBudget   *int64 `json:"thinkingBudget"`
			MaxTokens        int64  `json:"maxTokens"`
			MaxOutputTokens  int64  `json:"maxOutputTokens"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	metadata := make(map[string]UpstreamModelMetadata, len(catalog.Models))
	for id, entry := range catalog.Models {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		m := UpstreamModelMetadata{DisplayName: strings.TrimSpace(entry.DisplayName), Reasoning: entry.SupportsThinking}
		if entry.MaxTokens > 0 {
			m.ContextWindow = entry.MaxTokens
		}
		if entry.MaxOutputTokens > 0 {
			m.MaxOutputTokens = entry.MaxOutputTokens
		}
		if entry.SupportsImages != nil || entry.SupportsThinking != nil || entry.MaxTokens > 0 {
			m.InputModalities = []string{"text"}
			if entry.SupportsImages != nil && *entry.SupportsImages {
				m.InputModalities = append(m.InputModalities, "image")
			}
		}
		if entry.SupportsThinking != nil && *entry.SupportsThinking {
			level := "high"
			if entry.ThinkingBudget != nil {
				budget := *entry.ThinkingBudget
				if budget >= 0 && budget <= geminiThinkingBudgetLowMax {
					level = "low"
				} else if budget > geminiThinkingBudgetLowMax && budget <= geminiThinkingBudgetMediumMax {
					level = "medium"
				}
			}
			fixed := id[strings.LastIndex(id, "-")+1:]
			if fixed == "high" || fixed == "medium" || fixed == "low" {
				level = fixed
				m.SupportedReasoningLevels = []string{fixed}
			} else {
				m.SupportedReasoningLevels = []string{"low", "medium", "high"}
			}
			m.DefaultReasoningLevel = level
		}
		metadata[id] = m
	}
	return metadata, nil
}
