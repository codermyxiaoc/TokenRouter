package service

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

func validCanvasID(id string) bool { u, e := uuid.Parse(id); return e == nil && u.String() == id }

// 文档允许纯数据和受控节点；导入永远不运行脚本，也不自动抓取远程素材。
func ValidateCreativeCanvasDocument(raw json.RawMessage) (json.RawMessage, []string, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{"nodes":[],"connections":[],"viewport":{"x":0,"y":0,"k":1},"backgroundMode":"lines"}`)
	}
	if len(raw) > CreativeCanvasDocumentLimit {
		return nil, nil, ErrCanvasQuota
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, nil, ErrCanvasInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, nil, ErrCanvasInvalid
	}
	for k := range doc {
		if k != "nodes" && k != "connections" && k != "viewport" && k != "backgroundMode" {
			return nil, nil, ErrCanvasInvalid
		}
	}
	if background, exists := doc["backgroundMode"]; exists && background != "lines" && background != "dots" && background != "blank" {
		return nil, nil, ErrCanvasInvalid
	}
	nodes, ok := doc["nodes"].([]any)
	if !ok || len(nodes) > 1000 {
		return nil, nil, ErrCanvasInvalid
	}
	connections, ok := doc["connections"].([]any)
	if !ok || len(connections) > 3000 {
		return nil, nil, ErrCanvasInvalid
	}
	ids := map[string]bool{}
	assets := map[string]bool{}
	for _, item := range nodes {
		node, ok := item.(map[string]any)
		if !ok {
			return nil, nil, ErrCanvasInvalid
		}
		id, _ := node["id"].(string)
		typ, _ := node["type"].(string)
		if id == "" || len(id) > 128 || ids[id] {
			return nil, nil, ErrCanvasInvalid
		}
		ids[id] = true
		switch typ {
		case "text", "image", "video", "audio", "group", "config":
		default:
			return nil, nil, ErrCanvasInvalid
		}
		if err := validateCanvasValue(node, "", assets, 0); err != nil {
			return nil, nil, err
		}
	}
	for _, item := range connections {
		c, ok := item.(map[string]any)
		if !ok {
			return nil, nil, ErrCanvasInvalid
		}
		from, _ := c["from"].(string)
		to, _ := c["to"].(string)
		if from == "" {
			from, _ = c["source"].(string)
		}
		if from == "" {
			from, _ = c["fromNodeId"].(string)
		}
		if to == "" {
			to, _ = c["target"].(string)
		}
		if to == "" {
			to, _ = c["toNodeId"].(string)
		}
		if !ids[from] || !ids[to] || from == to {
			return nil, nil, ErrCanvasInvalid
		}
		if err := validateCanvasValue(c, "", assets, 0); err != nil {
			return nil, nil, err
		}
	}
	if err := validateCanvasValue(doc["viewport"], "viewport", assets, 0); err != nil {
		return nil, nil, err
	}
	clean, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, ErrCanvasInvalid
	}
	out := make([]string, 0, len(assets))
	for id := range assets {
		out = append(out, id)
	}
	return clean, out, nil
}

func validateCanvasValue(value any, key string, assets map[string]bool, depth int) error {
	if depth > 20 {
		return ErrCanvasInvalid
	}
	lower := strings.ToLower(key)
	switch lower {
	case "script", "code", "requestscript", "apikey", "api_key", "token", "accesstoken", "authorization", "baseurl", "base_url", "providerurl", "url", "src", "sourceurl", "outputurl", "dataurl", "html", "__proto__", "constructor", "prototype":
		return ErrCanvasInvalid
	}
	switch v := value.(type) {
	case map[string]any:
		for k, x := range v {
			if err := validateCanvasValue(x, k, assets, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(v) > 4000 {
			return ErrCanvasInvalid
		}
		for _, x := range v {
			if err := validateCanvasValue(x, key, assets, depth+1); err != nil {
				return err
			}
		}
	case string:
		if !utf8.ValidString(v) || len(v) > 65536 {
			return ErrCanvasInvalid
		}
		if lower == "asset_id" || lower == "asset_ids" || lower == "mask_asset_id" {
			if !validCanvasID(v) {
				return ErrCanvasInvalid
			}
			assets[v] = true
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "data:") || strings.HasPrefix(strings.TrimSpace(v), "blob:") {
			return ErrCanvasInvalid
		}
	case json.Number:
		f, e := v.Float64()
		if e != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 1e10 {
			return ErrCanvasInvalid
		}
	case bool, nil:
	default:
		return ErrCanvasInvalid
	}
	return nil
}

// 媒体按文件签名识别，不信任文件扩展名和客户端 Content-Type。
func CreativeCanvasMediaType(prefix []byte) (string, string, error) {
	mime := http.DetectContentType(prefix)
	switch mime {
	case "image/png", "image/jpeg", "image/webp":
		return "image", mime, nil
	case "video/webm":
		return "video", mime, nil
	case "audio/mpeg", "audio/wave", "audio/ogg":
		return "audio", mime, nil
	}
	// 无 ID3 元数据的合法 MP3 直接以 MPEG 音频帧开头，浏览器同样可播放。
	if creativeMP3FrameSize(prefix) > 0 {
		return "audio", "audio/mpeg", nil
	}
	if len(prefix) >= 12 && string(prefix[:4]) == "RIFF" && string(prefix[8:12]) == "WEBP" {
		return "image", "image/webp", nil
	}
	if len(prefix) >= 12 && string(prefix[4:8]) == "ftyp" {
		return "video", "video/mp4", nil
	}
	if len(prefix) >= 4 && bytes.Equal(prefix[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return "video", "video/webm", nil
	}
	return "", "", ErrCanvasInvalid
}
