package geminicli

// Model represents a selectable Gemini model for UI/testing purposes.
// Keep JSON fields consistent with existing frontend expectations.
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// DefaultModels is the curated Gemini model list used by the admin UI "test account" flow.
// 同步上游 Sub2API 31564b2e8：移除已停用的 2.x 与 3-pro-preview；gemini-3.5-flash-lite、3.7-flash、3.8-flash
// 在价格目录中没有官方价，暂不列入默认目录，避免按同族型号价格计费。
var DefaultModels = []Model{
	{ID: "gemini-3.1-flash-image", Type: "model", DisplayName: "Gemini 3.1 Flash Image", CreatedAt: ""},
	{ID: "gemini-3.1-flash-image-preview", Type: "model", DisplayName: "Gemini 3.1 Flash Image Preview", CreatedAt: ""},
	{ID: "gemini-3.1-flash-lite-image", Type: "model", DisplayName: "Gemini 3.1 Flash Lite Image", CreatedAt: ""},
	{ID: "gemini-3-pro-image", Type: "model", DisplayName: "Gemini 3 Pro Image", CreatedAt: ""},
	{ID: "gemini-3-pro-image-preview", Type: "model", DisplayName: "Gemini 3 Pro Image Preview", CreatedAt: ""},
	{ID: "gemini-3-flash", Type: "model", DisplayName: "Gemini 3 Flash", CreatedAt: ""},
	{ID: "gemini-3-flash-preview", Type: "model", DisplayName: "Gemini 3 Flash Preview", CreatedAt: ""},
	{ID: "gemini-3.1-pro-preview", Type: "model", DisplayName: "Gemini 3.1 Pro Preview", CreatedAt: ""},
	{ID: "gemini-3.1-pro-preview-customtools", Type: "model", DisplayName: "Gemini 3.1 Pro Preview Custom Tools", CreatedAt: ""},
	{ID: "gemini-3.1-flash-lite", Type: "model", DisplayName: "Gemini 3.1 Flash Lite", CreatedAt: ""},
	{ID: "gemini-3.1-flash-lite-preview", Type: "model", DisplayName: "Gemini 3.1 Flash Lite Preview", CreatedAt: ""},
	{ID: "gemini-3.5-flash", Type: "model", DisplayName: "Gemini 3.5 Flash", CreatedAt: ""},
	{ID: "gemini-3.6-flash", Type: "model", DisplayName: "Gemini 3.6 Flash", CreatedAt: ""},
	{ID: "gemini-4-argon", Type: "model", DisplayName: "Gemini 4 Argon", CreatedAt: ""},
}

// GoogleOneModels 是旧版 Google One OAuth 通道对外暴露的保守模型集合。
// 新版订阅模型通过 Antigravity OAuth 提供，不由已退役的消费者通道承载。
var GoogleOneModels = []Model{
	{ID: "gemini-2.5-flash", Type: "model", DisplayName: "Gemini 2.5 Flash", CreatedAt: ""},
	{ID: "gemini-2.5-pro", Type: "model", DisplayName: "Gemini 2.5 Pro", CreatedAt: ""},
	{ID: "gemini-2.0-flash", Type: "model", DisplayName: "Gemini 2.0 Flash", CreatedAt: ""},
}

// GoogleOneModelMapping 为每个账号返回新的白名单映射，避免调用方修改包级目录。
func GoogleOneModelMapping() map[string]string {
	mapping := make(map[string]string, len(GoogleOneModels))
	for _, model := range GoogleOneModels {
		mapping[model.ID] = model.ID
	}
	return mapping
}

// DefaultTestModel is the default model to preselect in test flows.
// 2.x 已被上游停用，测试默认改用目录中现役的 3.5 Flash（同步上游 Sub2API 31564b2e8）。
const DefaultTestModel = "gemini-3.5-flash"
