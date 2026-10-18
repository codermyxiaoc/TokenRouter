package service

import (
	"context"
	"math"
	"strings"
	"unicode/utf8"
)

// validateCreativeAudioParams 保留图片创建规则不变，为 Grok TTS 建立独立能力边界。
// @project-doc docs/domains/cloud_canvas.md#execution
func (s *CreativePublicService) validateCreativeAudioParams(ctx context.Context, group *Group, p *CreateCreativeRunParamsPublic) (*validatedCreativeParams, error) {
	if !p.canvasAudio {
		return nil, ErrCreativeOperationUnsupported
	}
	if group.Platform != PlatformGrok || strings.TrimSpace(p.Model) != CreativeGrokTTSModel || !s.creativeAudioConfigured(ctx, group.ID, p.Model) {
		return nil, ErrCreativeInvalidModel
	}
	eligible, err := s.creativeAudioGroupEligible(ctx, group)
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, ErrCreativeInvalidModel
	}
	if len(p.SourceImages) != 0 || p.Mask != nil || p.ImageSize != "" || p.AspectRatio != "" || p.Quality != "" || p.Background != "" || p.ThinkingLevel != "" || p.OutputCount < 0 || p.OutputCount > 1 || p.ExpectedOutputCount > 1 {
		return nil, ErrCreativeInvalidParams
	}
	prompt, voice, format, speed, err := normalizeCreativeAudioParams(p.Prompt, p.Voice, p.AudioFormat, p.Speed)
	if err != nil {
		return nil, err
	}
	hash := sha256Hex([]byte(prompt))
	fingerprint := buildCreativeRequestFingerprint(creativeFingerprintPayload{GroupID: group.ID, Model: CreativeGrokTTSModel, Operation: CreativeOperationTTS, PromptSHA256: hash, OutputCount: 1, Voice: voice, AudioFormat: format, Speed: speed})
	return &validatedCreativeParams{group: group, model: CreativeGrokTTSModel, finalModel: CreativeGrokTTSModel, operation: CreativeOperationTTS, prompt: prompt, promptHash: hash, outputCount: 1, voice: voice, audioFormat: format, speed: speed, fingerprint: fingerprint}, nil
}

// normalizeCreativeAudioParams 同时供准入和真正发送前校验，语言固定自动检测。
func normalizeCreativeAudioParams(text, voice, format string, speed float64) (string, string, string, float64, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) {
		return "", "", "", 0, ErrCreativeInvalidParams
	}
	if utf8.RuneCountInString(text) > 15000 {
		return "", "", "", 0, ErrCreativePromptTooLong
	}
	voice = strings.ToLower(strings.TrimSpace(voice))
	if voice == "" {
		voice = "eve"
	}
	switch voice {
	case "eve", "ara", "rex", "sal", "leo":
	default:
		return "", "", "", 0, ErrCreativeInvalidParams
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "mp3"
	}
	if format != "mp3" && format != "wav" {
		return "", "", "", 0, ErrCreativeInvalidParams
	}
	if speed == 0 {
		speed = 1
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < 0.7 || speed > 1.5 {
		return "", "", "", 0, ErrCreativeInvalidParams
	}
	return text, voice, format, speed, nil
}

func creativeValidatedResponseMIME(p *validatedCreativeParams) string {
	if p != nil && p.operation == CreativeOperationTTS {
		if p.audioFormat == "wav" {
			return "audio/wav"
		}
		return "audio/mpeg"
	}
	return defaultCreativeResponseMime
}

// creativeAudioBasePrice 复用正式 /tts 的连续单位计价，冻结单条音频整段文本的基础金额。
func (s *CreativePublicService) creativeAudioBasePrice(ctx context.Context, group *Group, text string) (float64, error) {
	units := float64(utf8.RuneCountInString(strings.TrimSpace(text))) / 1_000_000
	pricing := s.Pricing
	if pricing == nil {
		pricing = &BillingService{}
	}
	var cost *CostBreakdown
	if s.PricingResolver != nil {
		gid := group.ID
		resolved := s.PricingResolver.Resolve(ctx, PricingInput{Model: "tts", GroupID: &gid, Group: group})
		if resolved != nil && resolved.Mode == BillingModePerRequest {
			var err error
			cost, err = pricing.CalculateCostUnified(CostInput{Ctx: ctx, Model: "tts", GroupID: &gid, Group: group, UsageUnits: units, SizeTier: "tts", RateMultiplier: 1, Resolver: s.PricingResolver, Resolved: resolved})
			if err != nil {
				return 0, err
			}
		}
	}
	if cost == nil {
		cost = pricing.CalculateAudioCost("tts", units, &audioPriceConfig{TTSPerMChars: group.AudioTTSPricePerMillionChars}, 1)
	}
	if cost == nil || math.IsNaN(cost.TotalCost) || math.IsInf(cost.TotalCost, 0) || cost.TotalCost < 0 {
		return 0, ErrCreativeInvalidParams
	}
	return cost.TotalCost, nil
}
