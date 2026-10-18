package service

import (
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 这是本站云画布针对 H3 内嵌参考图的发送上限，不是供应商官方图片限制或素材存储上限。
const CreativeVideoH3ReferenceImageLimit int64 = 2 << 20

// 只检查内部可信原图长度，并遵循模型覆盖后的真实方案；旧任务恢复不经过新准入。
func validateCreativeVideoReferenceImageSize(target VideoUpstreamTarget, maxBytes int64) error {
	if maxBytes <= CreativeVideoH3ReferenceImageLimit {
		return nil
	}
	adapter, err := VideoTargetProtocolAdapter(target)
	if err != nil {
		return err
	}
	if adapter == VideoProtocolAdapterMetasoH3 {
		return infraerrors.BadRequest("CREATIVE_VIDEO_REFERENCE_TOO_LARGE", creativeCanvasFailureMessage("CREATIVE_VIDEO_REFERENCE_TOO_LARGE"))
	}
	return nil
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

// 仅明确选中的秘塔 H3 适配器消费画布内部自动比例提示。普通网关没有该提示，
// 其它协议保留 adaptive；转换后的 size 仍由原适配器产生并进入原价卡与任务快照。
func prepareCreativeVideoAdaptiveRatio(body []byte, target VideoUpstreamTarget, hint string) ([]byte, error) {
	if hint != "16:9" && hint != "9:16" {
		return body, nil
	}
	adapter, err := VideoTargetProtocolAdapter(target)
	if err != nil {
		return nil, err
	}
	if adapter != VideoProtocolAdapterMetasoH3 || gjson.GetBytes(body, "ratio").String() != "adaptive" {
		return body, nil
	}
	return sjson.SetBytes(body, "ratio", hint)
}
