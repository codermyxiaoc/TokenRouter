package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
)

// executeGrokAudio 不经过本地 HTTP 回环或网关后扣，资金由原 CreativeRun 账本管理。
// 发送后的未知结果保留执行栅栏，避免同步 TTS 没有任务 ID 时重复生成。
func (e *CreativeExecutor) executeGrokAudio(ctx context.Context, run CreativeRun, payload CreativeRunPayload, account *Account) ([]CreativeOutput, error) {
	if e.gateway == nil || e.gateway.httpUpstream == nil || len(payload.Sources) != 0 || payload.Mask != nil {
		return nil, creativeNonRetryableError("creative audio provider is unavailable")
	}
	text, voice, format, speed, err := normalizeCreativeAudioParams(payload.Prompt, payload.Voice, payload.AudioFormat, payload.Speed)
	if err != nil {
		return nil, creativeNonRetryableError("unsupported creative audio parameters")
	}
	expected := buildCreativeRequestFingerprint(creativeFingerprintPayload{GroupID: run.GroupID, Model: run.Model, Operation: CreativeOperationTTS, PromptSHA256: sha256Hex([]byte(text)), OutputCount: 1, Voice: voice, AudioFormat: format, Speed: speed})
	if sha256Hex([]byte(text)) != run.PromptHash || payload.RequestFingerprint != run.RequestFingerprint || expected != run.RequestFingerprint {
		return nil, creativeNonRetryableError("creative audio payload does not match reservation")
	}
	body, err := json.Marshal(map[string]any{"text": text, "voice_id": voice, "language": "auto", "speed": speed, "output_format": map[string]any{"codec": format}})
	if err != nil {
		return nil, creativeNonRetryableError("invalid creative audio body")
	}
	target, err := buildGrokVoiceURL(account, e.cfg, "tts")
	if err != nil {
		return nil, creativeNonRetryableError("invalid creative audio endpoint")
	}
	token, _, err := e.gateway.GetAccessToken(ctx, account)
	if err != nil {
		return nil, creativeHTTPStatusError(0, "credential unavailable")
	}
	requestCtx := WithHTTPUpstreamProfile(WithHTTPUpstreamRedirectsDisabled(ctx), HTTPUpstreamProfileGrok)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, creativeNonRetryableError("invalid creative audio request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg, audio/wav")
	if account.IsGrokOAuth() && isGrokCLIProxyTarget(target) {
		applyGrokCLIHeaders(req.Header)
	}
	account.ApplyHeaderOverrides(req.Header)
	resp, err := e.gateway.httpUpstream.Do(req, accountProxyURL(account), account.ID, account.Concurrency)
	if err != nil {
		return nil, creativeResultUnconfirmedError()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout {
		// 完整的拒绝状态可沿用失败释放；429 仍只做既有有限重试。
		return nil, creativeHTTPStatusError(resp.StatusCode, "audio request rejected")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, creativeResultUnconfirmedError()
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, creativeMaxOutputBytes+1))
	if err != nil || len(data) == 0 || len(data) > creativeMaxOutputBytes {
		return nil, creativeResultUnconfirmedError()
	}
	mime, valid := creativeAudioOutputMIME(data, format)
	if !valid {
		return nil, creativeResultUnconfirmedError()
	}
	return []CreativeOutput{{Index: 0, Bytes: data, Mime: mime}}, nil
}

// creativeAudioOutputMIME 校验可播放容器和完整音频帧，不把 JSON/HTML/只有文件头算成成品。
func creativeAudioOutputMIME(data []byte, format string) (string, bool) {
	if format == "wav" {
		if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
			return "", false
		}
		declared := uint64(binary.LittleEndian.Uint32(data[4:8])) + 8
		// 流式 WAV 可使用未知长度标志，其余大小必须已经完整返回。
		if declared != 0xffffffff+8 && declared > uint64(len(data)) {
			return "", false
		}
		seenFormat := false
		for offset := 12; offset+8 <= len(data); {
			n := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
			chunk := string(data[offset : offset+4])
			offset += 8
			if chunk == "data" && n == 0xffffffff {
				n = uint64(len(data) - offset)
			}
			if n > uint64(len(data)-offset) {
				return "", false
			}
			if chunk == "fmt " {
				seenFormat = n >= 16 && binary.LittleEndian.Uint16(data[offset:offset+2]) == 1 && binary.LittleEndian.Uint16(data[offset+2:offset+4]) > 0 && binary.LittleEndian.Uint32(data[offset+4:offset+8]) > 0
			}
			if chunk == "data" && seenFormat && n > 0 {
				return "audio/wav", true
			}
			offset += int(n + n%2)
		}
		return "", false
	}
	if format != "mp3" {
		return "", false
	}
	offset := 0
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		for _, b := range data[6:10] {
			if b&0x80 != 0 {
				return "", false
			}
		}
		offset = 10 + int(data[6])<<21 + int(data[7])<<14 + int(data[8])<<7 + int(data[9])
		if data[5]&0x10 != 0 {
			offset += 10
		}
	}
	if offset < 0 || offset+4 > len(data) {
		return "", false
	}
	frameSize := creativeMP3FrameSize(data[offset:])
	return "audio/mpeg", frameSize > 4 && offset+frameSize <= len(data)
}

// creativeMP3FrameSize 同时支持带 ID3 与直接音频帧的 MP3；仅按合法 MPEG Layer III 头计算长度。
func creativeMP3FrameSize(data []byte) int {
	if len(data) < 4 {
		return 0
	}
	h := data[:4]
	version, layer, bitrate, sample := (h[1]>>3)&3, (h[1]>>1)&3, h[2]>>4, (h[2]>>2)&3
	if h[0] != 0xff || h[1]&0xe0 != 0xe0 || version == 1 || layer != 1 || bitrate == 0 || bitrate == 15 || sample == 3 {
		return 0
	}
	rates := []int{44100, 48000, 32000}
	bitrates := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	coefficient, rate := 144000, rates[sample]
	if version != 3 {
		bitrates = []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
		coefficient, rate = 72000, rate/2
		if version == 0 {
			rate /= 2
		}
	}
	frameSize := coefficient*bitrates[bitrate]/rate + int((h[2]>>1)&1)
	return frameSize
}
