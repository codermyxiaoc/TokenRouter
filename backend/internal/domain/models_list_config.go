package domain

// GroupModelsListConfig 控制可选的自定义 /v1/models 响应列表。
type GroupModelsListConfig struct {
	Enabled bool     `json:"enabled"`
	Models  []string `json:"models,omitempty"`
}
