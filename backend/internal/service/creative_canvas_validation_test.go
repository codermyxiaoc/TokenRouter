package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCreativeCanvasDocumentAcceptsInfiniteCanvasShape(t *testing.T) {
	raw := json.RawMessage(`{"nodes":[{"id":"image-1","type":"image","metadata":{"asset_id":"11111111-1111-4111-8111-111111111111"}}],"connections":[],"viewport":{"x":1,"y":2,"k":1},"backgroundMode":"dots"}`)
	clean, ids, err := ValidateCreativeCanvasDocument(raw)
	require.NoError(t, err)
	require.Contains(t, string(clean), `"backgroundMode":"dots"`)
	require.Equal(t, []string{"11111111-1111-4111-8111-111111111111"}, ids)
}

func TestValidateCreativeCanvasDocumentRejectsRemoteAndExecutableValues(t *testing.T) {
	for _, raw := range []string{
		`{"nodes":[{"id":"x","type":"image","src":"https://evil.example/a.png"}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`,
		`{"nodes":[{"id":"x","type":"config","metadata":{"script":"alert(1)"}}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`,
		`{"nodes":[{"id":"x","type":"image","metadata":{"asset_id":"not-a-uuid"}}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`,
	} {
		_, _, err := ValidateCreativeCanvasDocument(json.RawMessage(raw))
		require.ErrorIs(t, err, ErrCanvasInvalid)
	}
}

func TestCreativeCanvasMediaTypeUsesMagicBytes(t *testing.T) {
	kind, mime, err := CreativeCanvasMediaType([]byte("\x89PNG\r\n\x1a\nrest"))
	require.NoError(t, err)
	require.Equal(t, "image", kind)
	require.Equal(t, "image/png", mime)
	kind, mime, err = CreativeCanvasMediaType([]byte("xxxxftypisomrest"))
	require.NoError(t, err)
	require.Equal(t, "video", kind)
	require.Equal(t, "video/mp4", mime)
}
