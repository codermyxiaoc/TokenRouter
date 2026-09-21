package middleware

import (
	"net/http"
	"strings"
)

// seedanceTaskPath 只识别已注册的 Ark 入口，防止宽泛前缀判断把其它路径误当成免消费查询。
func seedanceTaskPath(path string) (taskID string, matched bool) {
	path = strings.TrimSuffix(path, "/")
	for _, root := range []string{
		"/api/v3/contents/generations/tasks", "/v3/contents/generations/tasks",
		"/v1/contents/generations/tasks", "/contents/generations/tasks",
	} {
		if path == root {
			return "", true
		}
		if strings.HasPrefix(path, root+"/") {
			id := strings.TrimPrefix(path, root+"/")
			if id == "" || id == "." || id == ".." || strings.TrimSpace(id) != id || strings.ContainsAny(id, "/\\?#%\x00\r\n\t") {
				return "", false
			}
			return id, true
		}
	}
	return "", false
}

func isSeedanceTaskAPIPath(path string) bool {
	_, matched := seedanceTaskPath(path)
	return matched
}

func isSeedanceCreateRequest(method, path string) bool {
	id, matched := seedanceTaskPath(path)
	return matched && id == "" && method == http.MethodPost
}

// isSeedanceTaskManagementRequest 允许已创建任务查询和取消通过消费准入；
// 身份、Key 状态、IP、团队生命周期及 handler 中的任务归属仍必须校验。
func isSeedanceTaskManagementRequest(method, path string) bool {
	id, matched := seedanceTaskPath(path)
	return matched && id != "" && (method == http.MethodGet || method == http.MethodDelete)
}
