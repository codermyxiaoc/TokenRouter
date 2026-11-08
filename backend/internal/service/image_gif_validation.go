package service

import (
	"bytes"
	"image/gif"
)

// 完整校验 GIF 的上限：image.Decode 对 GIF 只解码第一帧，首帧完整、后续帧截断的动图会被误判为可交付成品。
// DecodeAll 会把所有帧放进内存：输入本身已受成品字节上限约束（创作台 32 MiB），解码后再按帧数与
// 累计像素预算拒绝超大动图，避免把过大的多帧结果交付或归档。
const (
	// maxGIFFrames 限制单个 GIF 的帧数。
	maxGIFFrames = 1000
	// maxGIFTotalPixels 限制所有帧的累计像素数（约 2 亿，按调色板图每像素 1 字节估算约 200 MiB）。
	maxGIFTotalPixels = 200_000_000
)

// isGIFData 按文件头判断是否为 GIF。
func isGIFData(data []byte) bool {
	return len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
}

// validGIFAllFrames 完整解码 GIF 的全部帧，任一帧缺失或截断即视为不可交付；
// 单帧画布超出预算时不进入全量解码。
func validGIFAllFrames(data []byte) bool {
	cfg, err := gif.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return false
	}
	framePixels := int64(cfg.Width) * int64(cfg.Height)
	if framePixels > maxGIFTotalPixels {
		return false
	}
	// 帧数只能在解码中得知：按每帧至多一整张画布估算，帧数上限由预算与固定上限共同决定。
	frameLimit := maxGIFTotalPixels / framePixels
	if frameLimit > maxGIFFrames {
		frameLimit = maxGIFFrames
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(decoded.Image) == 0 || int64(len(decoded.Image)) > frameLimit {
		return false
	}
	return true
}
