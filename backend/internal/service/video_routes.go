package service

import (
	"net/http"
	"strings"
)

// VideoGatewayRoute 是路由、鉴权和调度共用的精确描述，避免用 /videos 字符串误认 Kling。
type VideoGatewayRoute struct {
	Protocol     string
	Native       bool
	Create       bool
	Content      bool
	TaskID       string
	Model        string
	PathTemplate string
}

// MatchVideoGatewayRoute 不识别集合查询或嵌套未知路径，防止扩大免消费入口。
func MatchVideoGatewayRoute(method, path string) (VideoGatewayRoute, bool) {
	path = strings.TrimSuffix(path, "/")
	// OpenAI 视频入口与旧 Grok 共用 URL，平台分派由 HTTP 路由负责。
	for _, root := range []string{"/v1/videos", "/videos"} {
		if path == root && method == http.MethodPost {
			return VideoGatewayRoute{Protocol: "openai_videos", Create: true, PathTemplate: root}, true
		}
		if method == http.MethodGet {
			if strings.HasSuffix(path, "/content") {
				if id, ok := videoRouteID(strings.TrimSuffix(path, "/content"), root); ok {
					return VideoGatewayRoute{Protocol: "openai_videos", TaskID: id, Content: true}, true
				}
			} else if id, ok := videoRouteID(path, root); ok {
				return VideoGatewayRoute{Protocol: "openai_videos", TaskID: id}, true
			}
		}
	}
	for _, root := range []string{"/v1/video/generations", "/video/generations"} {
		if path == root && method == http.MethodPost {
			return VideoGatewayRoute{Protocol: "unified", Create: true}, true
		}
		if id, ok := videoRouteID(path, root); ok && (method == http.MethodGet || method == http.MethodDelete) {
			return VideoGatewayRoute{Protocol: "unified", TaskID: id}, true
		}
	}
	for _, root := range []string{"/v1/tasks"} {
		if id, ok := videoRouteID(path, root); ok && (method == http.MethodGet || method == http.MethodDelete) {
			return VideoGatewayRoute{Protocol: "unified", TaskID: id}, true
		}
	}
	for _, prefix := range []string{"/api/v3", "/v3", "/v1", ""} {
		root := prefix + "/contents/generations/tasks"
		if path == root && method == http.MethodPost {
			return VideoGatewayRoute{Protocol: "seedance", Native: true, Create: true}, true
		}
		if id, ok := videoRouteID(path, root); ok && (method == http.MethodGet || method == http.MethodDelete) {
			return VideoGatewayRoute{Protocol: "seedance", Native: true, TaskID: id}, true
		}
	}
	if method == http.MethodPost {
		for _, prefix := range []string{"/text-to-video/", "/image-to-video/", "/omni-video/"} {
			if strings.HasPrefix(path, prefix) && strings.TrimPrefix(path, prefix) != "" {
				return VideoGatewayRoute{Protocol: "kling", Native: true, Create: true, Model: strings.TrimPrefix(path, prefix), PathTemplate: prefix + "{model}"}, true
			}
		}
		for _, p := range []string{"/v1/videos/text2video", "/v1/videos/omni-video"} {
			if path == p {
				return VideoGatewayRoute{Protocol: "kling", Native: true, Create: true, PathTemplate: p}, true
			}
		}
		if path == "/api/v1/services/aigc/video-generation/video-synthesis" {
			return VideoGatewayRoute{Protocol: "wan", Native: true, Create: true}, true
		}
		if path == "/v2/video_generation" {
			return VideoGatewayRoute{Protocol: "minimax", Native: true, Create: true}, true
		}
	}
	if method == http.MethodGet {
		if path == "/tasks" {
			return VideoGatewayRoute{Protocol: "kling", Native: true}, true
		}
		if id, ok := videoRouteID(path, "/api/v1/tasks"); ok {
			return VideoGatewayRoute{Protocol: "wan", Native: true, TaskID: id}, true
		}
		if id, ok := videoRouteID(path, "/v2/query/video_generation"); ok {
			return VideoGatewayRoute{Protocol: "minimax", Native: true, TaskID: id}, true
		}
	}
	return VideoGatewayRoute{}, false
}

func videoRouteID(path, root string) (string, bool) {
	if !strings.HasPrefix(path, root+"/") {
		return "", false
	}
	id := strings.TrimPrefix(path, root+"/")
	return id, id != "" && strings.TrimSpace(id) == id && validateUpstreamPathSegment("video task ID", id) == nil
}
