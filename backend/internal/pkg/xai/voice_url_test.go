package xai

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
	"github.com/stretchr/testify/require"
)

func TestBuildVoiceURLNormalizesRootAndPreservesExplicitPrefix(t *testing.T) {
	validator := func(raw string) (string, error) { return urlvalidator.ValidateURLFormat(raw, true) }
	for _, tc := range []struct{ base, path string }{
		{"http://relay.example:8317", "/v1"}, {"http://relay.example:8317/", "/v1"},
		{"http://relay.example:8317/v1/", "/v1"}, {"http://relay.example:8317/proxy/xai/v1/", "/proxy/xai/v1"},
		{"http://relay.example:8317/custom", "/custom"},
	} {
		for _, ep := range []string{"tts", "stt", "realtime", "custom-voices", "custom-voices/voice-one/audio"} {
			got, err := BuildVoiceURLWithValidator(tc.base, ep, validator)
			require.NoError(t, err)
			require.Equal(t, "http://relay.example:8317"+tc.path+"/"+ep, got)
		}
	}
	got, err := BuildVoiceURLWithValidator("https://api.x.ai", "tts", validator)
	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/tts", got)
	_, err = BuildVoiceURLWithValidator("https://api.x.ai/custom", "tts", validator)
	require.Error(t, err)
}

func TestBuildVoiceURLRetainsPolicyAndRejectsAmbiguousBaseComponents(t *testing.T) {
	permissive := func(raw string) (string, error) { return raw, nil }
	for _, raw := range []string{"http://user:secret@relay.example", "http://relay.example?token=secret", "http://relay.example?", "http://relay.example#secret"} {
		_, err := BuildVoiceURLWithValidator(raw, "tts", permissive)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
	for _, ep := range []string{"", " ", "custom-voices/../audio", "custom-voices//audio", "custom-voices/./audio"} {
		_, err := BuildVoiceURLWithValidator("https://relay.example", ep, permissive)
		require.Error(t, err)
	}
	got, err := BuildVoiceURLWithValidator("https://relay.example", "custom-voices/voice name/audio", permissive)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example/v1/custom-voices/voice%20name/audio", got)
	called := false
	_, err = BuildVoiceURLWithValidator("http://relay.example", "tts", func(raw string) (string, error) { called = true; return urlvalidator.ValidateURLFormat(raw, false) })
	require.True(t, called)
	require.Error(t, err)
}
