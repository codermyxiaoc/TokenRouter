package service

import (
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

// 可美兼容完成包的 data 是产物数组；原生 Kling 的 data 是任务数组，不能混淆两种身份。
func videoProtocolResponseIdentityEndpoint(target VideoUpstreamTarget, body []byte) VideoEndpoint {
	adapter, err := VideoTargetProtocolAdapter(target)
	if err != nil || adapter != VideoProtocolAdapterKemei {
		return target.Endpoint
	}
	root := gjson.ParseBytes(body)
	if (target.Endpoint == VideoEndpointKling || target.Endpoint == VideoEndpointWan) && root.Get("status").Type == gjson.String &&
		(root.Get("id").Type == gjson.String || root.Get("task_id").Type == gjson.String) &&
		!root.Get("output").Exists() && !root.Get("outputs").Exists() {
		// 明确的平铺响应合同采用顶层任务 ID；data 中的文件 ID 不参与任务归属。
		data := root.Get("data")
		if !data.Exists() || (data.IsArray() && len(data.Array()) == 0) || data.Get("0.url").Type == gjson.String {
			return VideoEndpointCompat
		}
	}
	return target.Endpoint
}

// 可美文档声明未返回用量时会用全零占位，任何原生入口都不能据此免费完成 Token 任务。
func applyVideoKemeiResponse(out *VideoUpstreamResponse, root gjson.Result, target VideoUpstreamTarget, expectedTaskID string) {
	if target.Endpoint == VideoEndpointVidu {
		applyVideoKemeiViduResponse(out, root)
		return
	}
	endpoint := videoProtocolResponseIdentityEndpoint(target, out.Body)
	if target.Endpoint == VideoEndpointKling && endpoint == VideoEndpointKling {
		applyVideoOfficialKlingResponse(out, root, expectedTaskID)
		return
	}
	if target.Endpoint == VideoEndpointWan && endpoint == VideoEndpointWan {
		applyVideoOfficialWanResponse(out, root)
	}
	if target.Endpoint == VideoEndpointMiniMax && root.Get("task").IsObject() {
		root = root.Get("task")
	}
	out.Metadata.Tokens = nil
	usage := root.Get("usage")
	if usage.Exists() && !usage.IsObject() {
		out.MetadataError = "Invalid Kemei video usage"
		return
	}
	var observed *int64
	for _, field := range []string{"completion_tokens", "output_tokens"} {
		value := usage.Get(field)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.Number || value.Float() < 0 || value.Float() > math.MaxInt32 || math.Trunc(value.Float()) != value.Float() {
			out.MetadataError = "Invalid Kemei output token usage"
			return
		}
		n := value.Int()
		if observed != nil && *observed != n {
			out.MetadataError = "Conflicting Kemei output token usage"
			return
		}
		observed = &n
	}
	if observed != nil && *observed > 0 {
		out.Metadata.Tokens = observed
	}
	for _, path := range []string{"usage.output_seconds", "duration"} {
		if value := root.Get(path); value.Exists() {
			if seconds, ok := videoProtocolObservedSeconds(value, false); !ok {
				out.MetadataError = "Invalid Kemei output duration"
			} else if !(endpoint == VideoEndpointWan && (usage.Get("output_video_duration").Exists() || usage.Get("duration").Exists())) {
				// 统一完成包的实际输出秒数优先于请求时长；万相原生仍保持专属字段的优先级。
				out.Metadata.DurationSeconds = seconds
			}
			break
		}
	}
	if value := root.Get("resolution"); value.Exists() && (value.Type != gjson.String || strings.TrimSpace(value.String()) == "") {
		out.MetadataError = "Invalid Kemei output resolution"
	}
	if data := root.Get("data"); data.IsArray() && len(data.Array()) > 1 {
		// 现有报价以一个视频为单位，多产物不能仅取第一项扣费。
		out.MetadataError = "Multiple Kemei video outputs require explicit pricing support"
		out.VideoURL = ""
	}
}
