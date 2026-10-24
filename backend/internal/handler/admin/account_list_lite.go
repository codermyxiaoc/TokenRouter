package admin

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/handler/dto"
)

// 轻量列表只收窄嵌套分组，不删账号运行状态、配额、凭据存在性或高级调度评分。
// 账号编辑仍使用详情接口；列表的分组徽章只依赖这些摘要字段。
type accountListGroupSummary struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Platform       string  `json:"platform"`
	DisplayBrand   string  `json:"display_brand,omitempty"`
	RateMultiplier float64 `json:"rate_multiplier"`
	Status         string  `json:"status"`
}

type accountListGroupLink struct {
	AccountID int64                    `json:"account_id"`
	GroupID   int64                    `json:"group_id"`
	CreatedAt time.Time                `json:"created_at"`
	Group     *accountListGroupSummary `json:"group,omitempty"`
}

// 显式字段覆盖嵌入 Account 中的完整分组字段，避免重复序列化价格、路由和说明。
type accountListLiteItem struct {
	AccountWithConcurrency
	Groups        []*accountListGroupSummary `json:"groups,omitempty"`
	AccountGroups []accountListGroupLink     `json:"account_groups,omitempty"`
}

func accountListGroupFromDTO(group *dto.Group) *accountListGroupSummary {
	if group == nil {
		return nil
	}
	return &accountListGroupSummary{
		ID: group.ID, Name: group.Name, Platform: group.Platform,
		DisplayBrand: group.DisplayBrand, RateMultiplier: group.RateMultiplier, Status: group.Status,
	}
}

func accountListResponseItems(items []AccountWithConcurrency, lite bool) any {
	if !lite {
		return items
	}
	result := make([]accountListLiteItem, len(items))
	for i, item := range items {
		result[i].AccountWithConcurrency = item
		if item.Account == nil {
			continue
		}
		for _, group := range item.Groups {
			result[i].Groups = append(result[i].Groups, accountListGroupFromDTO(group))
		}
		for _, link := range item.AccountGroups {
			result[i].AccountGroups = append(result[i].AccountGroups, accountListGroupLink{
				AccountID: link.AccountID, GroupID: link.GroupID, CreatedAt: link.CreatedAt,
				Group: accountListGroupFromDTO(link.Group),
			})
		}
	}
	return result
}
