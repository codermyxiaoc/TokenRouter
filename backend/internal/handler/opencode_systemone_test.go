package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeSystemOneEndpoint(t *testing.T) {
	for _, path := range []string{"/v1/systemone", "/systemone", "/openai/v1/systemone"} {
		require.Equal(t, EndpointSystemOne, NormalizeInboundEndpoint(path), path)
	}
	require.Equal(t, EndpointSystemOne, DeriveUpstreamEndpoint(EndpointSystemOne, "/v1/systemone", "opencode_go"))
}
