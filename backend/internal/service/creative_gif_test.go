//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"testing"

	"github.com/stretchr/testify/require"
)

// newCreativeGIFTestService 构造一个只开放 GPT Image 2.6 与 gpt-image-2 的 OpenAI 分组。
func newCreativeGIFTestService() *CreativePublicService {
	svc := newCreativeTestService()
	group := newCreativeTestGroup()
	group.Platform = PlatformOpenAI
	svc.GroupRepo = &creativeFakeGroupRepo{byID: map[int64]*Group{group.ID: group}, active: []Group{*group}}
	svc.AccountRepo = &creativeFakeAccountRepo{byGroup: map[int64][]Account{group.ID: {{
		ID: 61, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"model_whitelist": []string{"gpt-image-2.6-flare", "gpt-image-2"}},
	}}}}
	svc.Settings = &creativeFakeSettingReader{enabled: true, models: []CreativeModelSetting{
		{GroupID: group.ID, Model: "gpt-image-2.6-flare", Operations: []string{CreativeOperationGenerate}},
		{GroupID: group.ID, Model: "gpt-image-2", Operations: []string{CreativeOperationGenerate}},
	}}
	return svc
}

func creativeTestGIF(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	frames := []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 4), palette), image.NewPaletted(image.Rect(0, 0, 4, 4), palette)}
	frames[1].SetColorIndex(1, 1, 1)
	var out bytes.Buffer
	require.NoError(t, gif.EncodeAll(&out, &gif.GIF{Image: frames, Delay: []int{10, 10}}))
	return out.Bytes()
}

// GPT Image 2.6 在创作台开放 png/gif 两种输出，其它 GPT 图片模型不开放格式选择。
func TestCreativeGIFOutputFormatCapability(t *testing.T) {
	svc := newCreativeGIFTestService()
	models, err := svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	formats := map[string][]string{}
	for _, model := range models.Data {
		formats[model.Model] = model.OutputFormats
	}
	require.Equal(t, []string{"png", "gif"}, formats["gpt-image-2.6-flare"])
	require.Empty(t, formats["gpt-image-2"])
}

// 只有开放 gif 的模型能选择 gif；png 规范化为空，旧任务的请求指纹与响应 MIME 不变。
func TestCreativeGIFOutputFormatValidation(t *testing.T) {
	svc := newCreativeGIFTestService()
	params := func(model, format string) *CreateCreativeRunParamsPublic {
		return &CreateCreativeRunParamsPublic{GroupID: 12, Model: model, Operation: CreativeOperationGenerate, Prompt: "cat", OutputFormat: format}
	}
	gifParams, err := svc.validateCreateParams(context.Background(), 7, params("gpt-image-2.6-flare", "GIF"))
	require.NoError(t, err)
	require.Equal(t, "gif", gifParams.outputFormat)
	require.Equal(t, "image/gif", creativeValidatedResponseMIME(gifParams))

	pngParams, err := svc.validateCreateParams(context.Background(), 7, params("gpt-image-2.6-flare", "png"))
	require.NoError(t, err)
	defaultParams, err := svc.validateCreateParams(context.Background(), 7, params("gpt-image-2.6-flare", ""))
	require.NoError(t, err)
	require.Empty(t, pngParams.outputFormat)
	require.Equal(t, defaultParams.fingerprint, pngParams.fingerprint, "显式 png 与默认值是同一请求")
	require.NotEqual(t, defaultParams.fingerprint, gifParams.fingerprint)
	require.Equal(t, "image/png", creativeValidatedResponseMIME(defaultParams))

	_, err = svc.validateCreateParams(context.Background(), 7, params("gpt-image-2", "gif"))
	require.ErrorIs(t, err, ErrCreativeInvalidParams)
	_, err = svc.validateCreateParams(context.Background(), 7, params("gpt-image-2.6-flare", "webp"))
	require.ErrorIs(t, err, ErrCreativeInvalidParams)
}

// 请求体按所选格式写入 output_format；未选择时保持 PNG。
func TestCreativeGIFRequestBody(t *testing.T) {
	run := CreativeRun{Operation: CreativeOperationGenerate, ImageSize: "1K", AspectRatio: "1:1"}
	body, _, err := buildCreativeOpenAIRequestBody(run, CreativeRunPayload{Prompt: "cat", OutputFormat: "gif"}, "gpt-image-2.6-flare")
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Equal(t, "gif", decoded["output_format"])

	body, _, err = buildCreativeOpenAIRequestBody(run, CreativeRunPayload{Prompt: "cat"}, "gpt-image-2.6-flare")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Equal(t, "png", decoded["output_format"])
}

// 上游返回的 GIF 成品可通过完整解码校验并以 image/gif 落库；画布按成品归档。
func TestCreativeGIFOutputIsAccepted(t *testing.T) {
	data := creativeTestGIF(t)
	output, ok := validatedCreativeImageOutput(data, 0)
	require.True(t, ok)
	require.Equal(t, "image/gif", output.Mime)
	kind, mime, err := CreativeCanvasMediaType(data)
	require.NoError(t, err)
	require.Equal(t, "image", kind)
	require.Equal(t, "image/gif", mime)
	require.Equal(t, "gif", creativeOutputFormatFromMIME("image/gif"))
}

// 参考图输入仍只接受 PNG/JPEG/WebP，GIF 不能借成品格式混入上游编辑请求。
func TestCreativeGIFRejectedAsSourceImage(t *testing.T) {
	data := creativeTestGIF(t)
	_, err := normalizeCreativeImageInput(CreativeInputImage{Bytes: data, Mime: "image/gif"}, 1<<20)
	require.ErrorIs(t, err, ErrCreativeInvalidMime)
	_, err = normalizeCreativeImageInput(CreativeInputImage{Bytes: data}, 1<<20)
	require.ErrorIs(t, err, ErrCreativeInvalidMime)
}
