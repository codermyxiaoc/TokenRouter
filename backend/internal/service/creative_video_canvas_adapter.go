package service

import (
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 这是本站云画布针对 H3 内嵌参考图的发送上限，不是供应商官方图片限制或素材存储上限。
const CreativeVideoH3ReferenceImageLimit int64 = 2 << 20

// 只检查内部可信原图长度，并遵循模型覆盖后的真实方案；旧任务恢复不经过新准入。
// 模板协议按 capabilities.max_reference_bytes 限制，旧 H3 方案沿用固定上限。
func validateCreativeVideoReferenceImageSize(target VideoUpstreamTarget, maxBytes int64) error {
	tooLarge := infraerrors.BadRequest("CREATIVE_VIDEO_REFERENCE_TOO_LARGE", creativeCanvasFailureMessage("CREATIVE_VIDEO_REFERENCE_TOO_LARGE"))
	if target.Version == videoTargetVersionProfile {
		caps, err := videoTargetProfileCapabilities(target)
		if err != nil {
			return err
		}
		if caps != nil && caps.MaxReferenceBytes > 0 && maxBytes > caps.MaxReferenceBytes {
			return tooLarge
		}
		return nil
	}
	if maxBytes <= CreativeVideoH3ReferenceImageLimit {
		return nil
	}
	adapter, err := VideoTargetProtocolAdapter(target)
	if err != nil {
		return err
	}
	if adapter == VideoProtocolAdapterMetasoH3 {
		return tooLarge
	}
	return nil
}

// videoTargetProfileCapabilities 返回模板快照声明的能力；内置协议返回 nil。
func videoTargetProfileCapabilities(target VideoUpstreamTarget) (*VideoProfileCapabilities, error) {
	if target.Version != videoTargetVersionProfile {
		return nil, nil
	}
	spec, err := videoProfileSpecFromTarget(target)
	if err != nil {
		return nil, err
	}
	return spec.Capabilities, nil
}

// 画布自动比例先从已经过归属、格式与尺寸校验的参考图取方向；没有参考图时沿用
// 模型明确的横竖屏默认值，其它默认比例使用横屏。这里只生成内部提示，不改画布参数。
func creativeVideoCanvasAdaptiveRatio(e *CreativeCanvasExecution, model *CreativeModelPublic, assets []CreativeCanvasInputAsset) (string, error) {
	if e.Params.AspectRatio != "adaptive" {
		return "", nil
	}
	if len(assets) > 0 {
		asset := assets[0]
		if asset.Asset == nil {
			return "", ErrCanvasInvalid
		}
		width, height, err := creativeImageDimensions(asset.Content, asset.Asset.MIMEType)
		if err != nil || width <= 0 || height <= 0 {
			return "", ErrCreativeInvalidMime
		}
		if height > width {
			return "9:16", nil
		}
		return "16:9", nil
	}
	if model.VideoCapabilities != nil && model.VideoCapabilities.DefaultAspectRatio == "9:16" {
		return "9:16", nil
	}
	return "16:9", nil
}

// 仅明确选择的协议消费画布内部自动比例提示。普通网关没有该提示；
// H3 解析横竖屏，Yezonghui 省略比例让上游自动选择，其它协议保持原行为。
func prepareCreativeVideoAdaptiveRatio(body []byte, target VideoUpstreamTarget, hint string) ([]byte, error) {
	if hint != "16:9" && hint != "9:16" {
		return body, nil
	}
	if target.Version == videoTargetVersionProfile {
		// 模板协议的发送前正文是规范请求，比例位于 aspect_ratio，由 capabilities.adaptive_ratio 决定换算或省略。
		caps, err := videoTargetProfileCapabilities(target)
		if err != nil {
			return nil, err
		}
		if caps == nil || gjson.GetBytes(body, "aspect_ratio").String() != "adaptive" {
			return body, nil
		}
		switch caps.AdaptiveRatio {
		case "orientation":
			return sjson.SetBytes(body, "aspect_ratio", hint)
		case "omit":
			return sjson.DeleteBytes(body, "aspect_ratio")
		}
		return body, nil
	}
	adapter, err := VideoTargetProtocolAdapter(target)
	if err != nil {
		return nil, err
	}
	if gjson.GetBytes(body, "ratio").String() != "adaptive" {
		return body, nil
	}
	switch adapter {
	case VideoProtocolAdapterMetasoH3:
		return sjson.SetBytes(body, "ratio", hint)
	case VideoProtocolAdapterYezonghui:
		return sjson.DeleteBytes(body, "ratio")
	default:
		return body, nil
	}
}
