package service

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// 可灵原生接口的声音是枚举而非布尔值，回调与水印属于 options；冲突不能静默覆盖。
func videoKemeiKlingSettings(values map[string]json.RawMessage) error {
	settings, err := videoProtocolObject(values, "settings")
	if err != nil {
		return err
	}
	options, err := videoProtocolObject(values, "options")
	if err != nil {
		return err
	}
	for _, field := range []string{"duration", "resolution", "audio", "multi_shot"} {
		if err = videoProtocolMove(values, settings, field, field); err != nil {
			return err
		}
	}
	if err = videoProtocolMove(values, settings, "ratio", "aspect_ratio"); err != nil {
		return err
	}
	if value, exists := values["generate_audio"]; exists {
		v := gjson.ParseBytes(value)
		if v.Type != gjson.True && v.Type != gjson.False {
			return videoProtocolError("Kling generate_audio must be a boolean")
		}
		audio := "off"
		if v.Bool() {
			audio = "native"
		}
		mapped := map[string]json.RawMessage{}
		videoProtocolPut(mapped, "audio", audio)
		if err = videoProtocolMove(mapped, settings, "audio", "audio"); err != nil {
			return err
		}
		delete(values, "generate_audio")
	}
	for _, field := range []string{"callback_url", "external_task_id"} {
		if err = videoProtocolMove(values, options, field, field); err != nil {
			return err
		}
	}
	if raw, exists := values["watermark"]; exists {
		v := gjson.ParseBytes(raw)
		if v.Type != gjson.True && v.Type != gjson.False {
			return videoProtocolError("Kling watermark must be a boolean")
		}
		watermark, objectErr := videoProtocolObject(options, "watermark_info")
		if objectErr != nil {
			return objectErr
		}
		if err = videoProtocolMove(values, watermark, "watermark", "enabled"); err != nil {
			return err
		}
		videoProtocolPut(options, "watermark_info", watermark)
	}
	// 默认计价维度写入发送正文，避免上游采用默认值而本地无从报价。
	for field, value := range map[string]any{"duration": 5, "resolution": "720p", "aspect_ratio": "16:9", "audio": "off"} {
		if _, exists := settings[field]; !exists {
			videoProtocolPut(settings, field, value)
		}
	}
	root := videoProtocolRoot(settings)
	audio := root.Get("audio")
	if audio.Type != gjson.String || (audio.String() != "off" && audio.String() != "native" && audio.String() != "original") {
		return videoProtocolError("Kling audio must be off, native or original")
	}
	if ratio := root.Get("aspect_ratio"); ratio.Type != gjson.String || !videoAccountTestRatio.MatchString(ratio.String()) || ratio.String() == "adaptive" {
		return videoProtocolError("Kling aspect_ratio must be a positive ratio")
	}
	if multi := root.Get("multi_shot"); multi.Exists() && multi.Type != gjson.True && multi.Type != gjson.False {
		return videoProtocolError("Kling multi_shot must be a boolean")
	}
	for _, item := range gjson.ParseBytes(values["contents"]).Array() {
		switch item.Get("type").String() {
		case "feature_video":
			if audio.String() != "off" {
				return videoProtocolError("Kling feature_video requires audio off")
			}
		case "base_video":
			if audio.String() == "native" || root.Get("multi_shot").Bool() {
				return videoProtocolError("Kling base_video does not support native audio or multi-shot")
			}
			// 编辑模式的原生默认是关闭多镜头，明确写入以防上游采用生成模式默认值。
			videoProtocolPut(settings, "multi_shot", false)
		}
	}
	videoProtocolPut(values, "settings", settings)
	if len(options) > 0 {
		videoProtocolPut(values, "options", options)
	}
	return nil
}

// 新版 Omni 只接受官方素材类型；元素内部图片数未知时保留 nil，禁止当作零张计费。
func videoKemeiKlingContentCount(contents gjson.Result, imageOperation bool) (*int, bool, error) {
	if !contents.IsArray() || len(contents.Array()) == 0 {
		return nil, false, videoProtocolError("Kling contents must be a non-empty array")
	}
	count, first, last, feature, base := 0, 0, 0, 0, 0
	unknown := false
	for _, item := range contents.Array() {
		if !item.IsObject() || item.Get("type").Type != gjson.String {
			return nil, false, videoProtocolError("Kling contents require typed objects")
		}
		typ := item.Get("type").String()
		switch typ {
		case "prompt":
			if item.Get("text").Type != gjson.String || strings.TrimSpace(item.Get("text").String()) == "" {
				return nil, false, videoProtocolError("Kling prompt content requires text")
			}
		case "first_frame", "last_frame", "refer_image", "feature_video", "base_video":
			if item.Get("url").Type != gjson.String || strings.TrimSpace(item.Get("url").String()) == "" {
				return nil, false, videoProtocolError("Kling media content requires URL or Base64")
			}
			switch typ {
			case "first_frame":
				first++
				count++
			case "last_frame":
				last++
				count++
			case "refer_image":
				count++
			case "feature_video":
				feature++
			case "base_video":
				base++
			}
		case "element":
			if item.Get("element_id").Type != gjson.String || strings.TrimSpace(item.Get("element_id").String()) == "" {
				return nil, false, videoProtocolError("Kling element requires element_id")
			}
			unknown = true
		default:
			return nil, false, videoProtocolError("Unsupported Kemei Kling content type")
		}
		if imageOperation && typ != "prompt" && typ != "first_frame" && typ != "last_frame" {
			return nil, false, videoProtocolError("Kling image-to-video only accepts first/last frames")
		}
	}
	if first > 1 || last > 1 || (last > 0 && first == 0) || feature+base > 1 || (base > 0 && first+last > 0) || (imageOperation && first != 1) {
		return nil, false, videoProtocolError("Conflicting Kling media or operation")
	}
	if unknown {
		return nil, feature+base > 0, nil
	}
	return &count, feature+base > 0, nil
}
