//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCreateGeminiTestPayload_ImageModel(t *testing.T) {
	t.Parallel()

	payload := createGeminiTestPayload("gemini-2.5-flash-image", "draw a tiny robot")

	var parsed struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseModalities []string `json:"responseModalities"`
			ImageConfig        struct {
				AspectRatio string `json:"aspectRatio"`
			} `json:"imageConfig"`
		} `json:"generationConfig"`
	}

	require.NoError(t, json.Unmarshal(payload, &parsed))
	require.Len(t, parsed.Contents, 1)
	require.Len(t, parsed.Contents[0].Parts, 1)
	require.Equal(t, "draw a tiny robot", parsed.Contents[0].Parts[0].Text)
	require.Equal(t, []string{"TEXT", "IMAGE"}, parsed.GenerationConfig.ResponseModalities)
	require.Equal(t, "1:1", parsed.GenerationConfig.ImageConfig.AspectRatio)
}

func TestCreateGeminiTestPayload_ExplicitTypeOverridesModelName(t *testing.T) {
	t.Parallel()

	textPayload := createGeminiTestPayload("gemini-2.5-flash-image", "reply briefly", AccountTestTypeText)
	var textParsed map[string]any
	require.NoError(t, json.Unmarshal(textPayload, &textParsed))
	require.NotContains(t, textParsed, "generationConfig")
	require.Equal(t, "reply briefly", textParsed["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"])

	imagePayload := createGeminiTestPayload("gemini-2.5-flash", "draw a tiny robot", AccountTestTypeImage)
	var imageParsed map[string]any
	require.NoError(t, json.Unmarshal(imagePayload, &imageParsed))
	config := imageParsed["generationConfig"].(map[string]any)
	require.Equal(t, []any{"TEXT", "IMAGE"}, config["responseModalities"])
}

func TestProcessGeminiStream_EmitsImageEvent(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	ctx, recorder := newTestContext()
	svc := &AccountTestService{}

	stream := strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"},{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"QUJD\"}}]}}]}\n\ndata: [DONE]\n\n")

	err := svc.processGeminiStream(ctx, stream)
	require.NoError(t, err)

	body := recorder.Body.String()
	require.Contains(t, body, "\"type\":\"content\"")
	require.Contains(t, body, "\"text\":\"ok\"")
	require.Contains(t, body, "\"type\":\"image\"")
	require.Contains(t, body, "\"image_url\":\"data:image/png;base64,QUJD\"")
	require.Contains(t, body, "\"mime_type\":\"image/png\"")
}
