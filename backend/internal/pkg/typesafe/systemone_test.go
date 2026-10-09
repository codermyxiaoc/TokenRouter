package typesafe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// 计费和审核必须与上游看到同一请求，拒绝重复键和大小写解析分歧。
func TestSystemOneRejectsAmbiguousJSON(t *testing.T) {
	for _, body := range []string{
		`{"model":"jev-1.13-free","model":"jev-latest","state":"x","questions":{"q":{"type":"noul"}}}`,
		`{"Model":"jev-latest","state":"x","questions":{"q":{"type":"noul"}}}`,
		`{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul"},"q":{"type":"noul"}}}`,
		`{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul","Type":"choice"}}}`,
		`{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul"}},"stream":true}`,
	} {
		_, err := ValidateSystemOneRequest([]byte(body))
		require.Error(t, err, body)
	}
	for _, model := range []string{"jev-latest", "jev-1.13", "jev-1.13-free"} {
		got, err := ValidateSystemOneRequest([]byte(`{"model":"` + model + `","state":{"messages":["x"]},"questions":{"q":{"type":"choice","criteria":{}}}}`))
		require.NoError(t, err)
		require.Equal(t, model, got)
	}
}

func TestEvaluateVersionedBaseAndStringUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/systemone", r.URL.Path)
		_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{"q":{"type":"noul","noul":0.1}},"usage":{"input_tokens":"15","output_tokens":3}}`))
	}))
	defer server.Close()
	result, _, err := Evaluate(context.Background(), server.Client(), server.URL+"/v1/", "secret", Request{Model: "jev-latest", State: "x", Questions: map[string]Question{"q": {Type: "noul"}}})
	require.NoError(t, err)
	require.Equal(t, 15, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
}
