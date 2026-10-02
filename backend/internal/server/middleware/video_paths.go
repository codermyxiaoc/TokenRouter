package middleware

import (
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 视频路径使用同一明确描述，原生请求不应用文本附加模型和响应别名转换。
func isVideoGatewayAPIPath(path string) bool {
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if _, ok := service.MatchVideoGatewayRoute(method, path); ok {
			return true
		}
	}
	return false
}

// 共享 /videos 入口仅在选择 Video 分组后保留厂商扩展字段，旧 Grok 继续原模型恢复规则。
func isVideoPayloadPreservingPath(path string, group *service.Group) bool {
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if route, ok := service.MatchVideoGatewayRoute(method, path); ok {
			return route.Protocol != "openai_videos" || (group != nil && group.Platform == service.PlatformVideo)
		}
	}
	return false
}

func isVideoTaskManagementRequest(method, path string) bool {
	route, ok := service.MatchVideoGatewayRoute(method, path)
	if ok && route.Protocol == "openai_videos" {
		// 新平台只接受本站任务 ID，不能扩大旧 Grok 普通 Key 的消费准入豁免。
		return !route.Create && strings.HasPrefix(route.TaskID, "vid_")
	}
	return ok && !route.Create
}

// 新共享路径的普通 Key 只对 Video 分组免除消费准入；同名前缀的旧 Grok 任务保持原规则。
func isVideoTaskBillingBypassRequest(method, path string, key *service.APIKey) bool {
	if !isVideoTaskManagementRequest(method, path) {
		return false
	}
	route, _ := service.MatchVideoGatewayRoute(method, path)
	return route.Protocol != "openai_videos" || (key != nil &&
		(key.IsComposite || key.SmartRouting || (key.Group != nil && key.Group.Platform == service.PlatformVideo)))
}

func videoRequestPathModel(request *http.Request) string {
	if request == nil {
		return ""
	}
	route, ok := service.MatchVideoGatewayRoute(request.Method, request.URL.Path)
	if ok && route.Create {
		return route.Model
	}
	return ""
}

func rewriteVideoRequestPathModel(request *http.Request, model string) error {
	route, _ := service.MatchVideoGatewayRoute(request.Method, request.URL.Path)
	request.URL.Path = strings.ReplaceAll(route.PathTemplate, "{model}", model)
	request.URL.RawPath = ""
	body, err := readAndRestoreRequestBody(request)
	if err != nil {
		return err
	}
	if gjson.GetBytes(body, "model").Exists() {
		body, err = sjson.SetBytes(body, "model", model)
		if err != nil {
			return err
		}
		setRequestBody(request, body)
	}
	return nil
}
