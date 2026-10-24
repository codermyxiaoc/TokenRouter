package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

// normalizeAccountQuotaLimits 只校验本次明确提交的上限，不能因更新无关字段拒绝历史记录。
// 与既有读取契约一致：缺省、null 和非正数不启用额度；有限数字字符串归一后保存，
// 防止 Go 把畸形值读成零而 PostgreSQL 在结算时因 numeric 转换失败回滚账务。
// @project-doc docs/domains/routing_and_billing.md#usage_settlement
func normalizeAccountQuotaLimits(extra map[string]any) error {
	for _, key := range []string{"quota_limit", "quota_daily_limit", "quota_weekly_limit"} {
		raw, exists := extra[key]
		if !exists || raw == nil {
			continue
		}
		var value float64
		var err error
		switch typed := raw.(type) {
		case float64:
			value = typed
		case float32:
			value = float64(typed)
		case int:
			value = float64(typed)
		case int64:
			value = float64(typed)
		case json.Number:
			value, err = typed.Float64()
		case string:
			value, err = strconv.ParseFloat(strings.TrimSpace(typed), 64)
		default:
			err = fmt.Errorf("not a number")
		}
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return infraerrors.BadRequest("INVALID_ACCOUNT_QUOTA_LIMIT", key+" must be a finite number or null")
		}
		extra[key] = value
	}
	return nil
}
