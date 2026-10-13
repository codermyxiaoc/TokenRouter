package service

import (
	"encoding/json"
	"time"
)

// dailyActivityPublicEnabled 只公开入口开关，损坏或尚未启用过的配置保持隐藏。
func dailyActivityPublicEnabled(raw string) bool {
	var value struct {
		Enabled   bool       `json:"enabled"`
		StartedAt *time.Time `json:"started_at"`
	}
	return json.Unmarshal([]byte(raw), &value) == nil && value.Enabled && value.StartedAt != nil && !value.StartedAt.IsZero()
}
