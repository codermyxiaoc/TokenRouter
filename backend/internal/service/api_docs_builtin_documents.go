package service

// builtinAPIDocDocumentCategories 是内置文档的默认分类，必须与前端 content/api-docs 的内置文档一致（前端测试会核对）。
// 内置文档的叠加行 category_id 为空表示沿用默认分类，公开读取正文时据此判断所属分类及其上级是否隐藏。
var builtinAPIDocDocumentCategories = map[string]string{
	"quickstart": "guides", "auth": "guides", "overview": "guides", "errors": "guides",
	"messages": "text", "chat-completions": "text", "responses": "text", "responses-compact": "text",
	"responses-websocket": "text", "embeddings": "text", "gemini-generate-content": "text",
	"gemini-stream-content": "text", "antigravity-messages": "text", "gemini-cache-generate": "text",
	"messages-count-tokens": "tools", "responses-input-tokens": "tools", "gemini-count-tokens": "tools",
	"opencode-systemone": "tools", "alpha-search": "tools", "gemini-cache-create": "tools",
	"gemini-cache-list": "tools", "gemini-cache-get": "tools", "gemini-cache-update": "tools",
	"gemini-cache-delete": "tools", "grok-web-search": "tools", "grok-x-search": "tools",
	"models-list": "models", "models-retrieve": "models", "key-usage": "models", "gemini-models-list": "models",
	"gemini-models-retrieve": "models", "antigravity-models": "models", "antigravity-model-retrieve": "models",
	"gemini-images": "images", "images-generate": "images", "images-edit": "images",
	"images-generate-async": "images", "images-edit-async": "images", "images-task": "images",
	"image-batch-create": "images", "image-batch-models": "images", "image-batch-list": "images",
	"image-batch-get": "images", "image-batch-items": "images", "image-batch-content": "images",
	"image-batch-download": "images", "image-batch-cancel": "images", "image-batch-delete": "images",
	"image-batch-delete-outputs": "images",
	"grok-tts":                   "audio", "grok-stt": "audio", "grok-voice-create": "audio", "grok-voice-list": "audio",
	"grok-voice-get": "audio", "grok-voice-update": "audio", "grok-voice-delete": "audio",
	"grok-voice-audio": "audio", "grok-realtime": "audio", "openai-live": "audio",
	"openai-live-sideband": "audio",
	"video-create":         "video-openai", "video-query": "video-openai", "video-content": "video-openai",
	"video-retired-endpoints": "video-openai", "video-yezonghui-create": "video-openai",
	"grok-video-create": "video-grok", "grok-video-edit": "video-grok", "grok-video-extend": "video-grok",
	"grok-video-query": "video-grok", "grok-video-content": "video-grok",
}
