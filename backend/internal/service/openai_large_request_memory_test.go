package service

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func largeNativeResponsesBody(imageBytes int) []byte {
	prefix := []byte(`{"model":"gpt-6-astra","store":false,"stream":true,"input":[{"type":"custom_tool_call_output","id":"fc_valid","call_id":"call_valid","output":[{"type":"input_image","image_url":"data:image/png;base64,`)
	suffix := []byte(`"}]}]}`)
	body := make([]byte, 0, len(prefix)+imageBytes+len(suffix))
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte{'A'}, imageBytes)...)
	return append(body, suffix...)
}

func TestLargeNativeResponsesRemainsByteIdentical(t *testing.T) {
	body := largeNativeResponsesBody(2 << 20)
	for _, fn := range []func([]byte) ([]byte, bool, error){
		normalizeOpenAIResponsesLegacyIngress,
		sanitizeOpenAIResponsesInputItemIDs,
		normalizeOpenAIResponsesReasoningContentReplay,
		func(body []byte) ([]byte, bool, error) {
			return normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, false)
		},
	} {
		out, changed, err := fn(body)
		require.NoError(t, err)
		require.False(t, changed)
		require.True(t, bytes.Equal(body, out))
		require.Same(t, &body[0], &out[0], "无修改路径必须复用原始只读切片")
	}
}

func TestRawInputRewritePreservesImagesAndSurroundingFields(t *testing.T) {
	body := []byte(" \n\t" + string(largeNativeResponsesBody(2<<20)))
	body = bytes.Replace(body, []byte(`"id":"fc_valid"`), []byte(`"id":"bad"`), 1)
	body = append(body[:len(body)-1], []byte(`,"opaque":9007199254740993}`)...)
	image := gjson.GetBytes(body, "input.0.output.0.image_url").String()
	original := bytes.Clone(body)
	out, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, gjson.ValidBytes(out))
	require.False(t, gjson.GetBytes(out, "input.0.id").Exists())
	require.Equal(t, image, gjson.GetBytes(out, "input.0.output.0.image_url").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(out, "opaque").Raw)
	require.Equal(t, "call_valid", gjson.GetBytes(out, "input.0.call_id").String())
	require.Equal(t, original, body, "智能路由和异步计费保存的原请求不能被修改")
}

func TestStoreFalseRawReplayMatchesDecodedBehavior(t *testing.T) {
	fixtures := []string{
		` {"store":false,"input":[{"type":"reasoning","id":"rs_a","encrypted_content":"opaque","summary":null,"opaque":9007199254740993},{"type":"custom_tool_call_output","id":"fc_a","output":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}],"tail":"keep"} `,
		`{"store":false,"input":[null,42,"keep",{"type":"reasoning","encrypted_content":" "},{"type":"item_reference","id":"rs_drop"},{"type":"item_reference","id":"msg_keep"}]}`,
		`{"store":false,"input":[{"type":"reasoning","id":"keep","encrypted_content":"opaque","summary":[]},{"type":"message","call_id":null,"content":"keep"}]}`,
		`{"store":false,"input":[],"input":[{"type":"reasoning","id":"rs_drop"}]}`,
		`{"store":false,"input":[{"type":"message","type":"reasoning","id":"rs_drop"}]}`,
		`{"store":false,"input":[{"type":"reasoning","id":"rs_drop"}]} trailing`,
		`{"store":false,"input":[{"type":"reasoning","id":"rs_drop"}]`,
		`{"store":false,"input":[]}`,
		`{"store":false,"input":[{"type":"reasoning","encrypted_content":"opaque","summary":[]},{"type":"function_call","namespace":"tools","name":"spawn_agent","call_id":"call_a","arguments":"{}"}],"tools":[{"type":"namespace","name":"tools","tools":[]}],"prompt_cache_key":"keep-session"}`,
		`{"store":false,"input":[{"type":"reasoning","encrypted_content":"opaque","summary":null},{"type":"message","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}],"additional_tools":[{"type":"namespace","name":"lite","tools":[]}]}]}`,
		"{\"store\":false,\"input\":[{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\",\"summary\":null},{\"type\":\"message\",\"content\":\"bad-\xff-utf8\"}]}",
	}
	for _, fixture := range fixtures {
		body := []byte(fixture)
		original := bytes.Clone(body)
		want, wantChanged, wantErr := normalizeOpenAIAPIKeyStoreFalseReasoningReplayDecoded(body, false)
		got, changed, err := normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, false)
		require.Equal(t, wantErr != nil, err != nil)
		if wantErr != nil {
			continue
		}
		require.Equal(t, wantChanged, changed)
		require.JSONEq(t, string(want), string(got))
		require.Equal(t, original, body)
	}
}

// 对照优化前实现，确保 ID 清理、旧格式兼容和推理处理不改变错误或协议语义。
func TestLargeResponsesMemoryFastPathsMatchBaseline(t *testing.T) {
	fixtures := []string{
		`{"model":"gpt-6-astra","input":[]}`,
		` {"input":[{"type":"message","id":"invalid","call_id":null,"content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}],"opaque":9007199254740993} `,
		`{"input":[{"type":"function_call","id":"bad","namespace":"tools","name":"spawn_agent","call_id":"keep","arguments":"{}"},{"type":"custom_tool_call_output","id":"bad","call_id":"keep","output":"keep"}],"tools":[{"type":"namespace","name":"tools","tools":[]}]}`,
		`{"input":[{"type":"reasoning","id":"rs_keep","encrypted_content":"opaque","content":[{"type":"reasoning_text","text":"remove"}],"summary":[]}],"prompt_cache_key":"session","metadata":{"user_id":"keep"}}`,
		`{"input":[],"input":[{"type":"message","id":"bad","content":"last"}]}`,
		`{"input":[{"type":"message","type":"function_call","id":"bad","namespace":"tools","call_id":"keep"}]}`,
		`{"input":[],"mess\u0061ges":[]}`,
		`{"messages":[{"role":"user","content":"legacy"}],"commands":["remove"],"opaque":9007199254740993}`,
		`{"input":"native","prompt":"legacy"}`,
		`{"input":"native","prompt":{"id":"template"}}`,
		`{"input":[{"type":"message","id":"bad"}]} trailing`,
		`{"input":[{"type":"message","id":"bad"}]`,
		`null`,
		`[]`,
		"{\"input\":[{\"type\":\"message\",\"id\":\"bad\",\"content\":\"invalid-\xff\"}]}",
	}
	for _, pair := range []struct {
		name          string
		before, after func([]byte) ([]byte, bool, error)
	}{
		{"legacy", memoryBaselineLegacyIngress, normalizeOpenAIResponsesLegacyIngress},
		{"ids", memoryBaselineInputItemIDs, sanitizeOpenAIResponsesInputItemIDs},
		{"reasoning", memoryBaselineReasoningContent, normalizeOpenAIResponsesReasoningContentReplay},
	} {
		t.Run(pair.name, func(t *testing.T) {
			for _, fixture := range fixtures {
				body := []byte(fixture)
				original := bytes.Clone(body)
				want, wantChanged, wantErr := pair.before(body)
				got, gotChanged, gotErr := pair.after(body)
				require.Equal(t, wantErr != nil, gotErr != nil, fixture)
				require.Equal(t, wantChanged, gotChanged, fixture)
				if wantErr == nil {
					if gjson.ValidBytes(want) && gjson.ValidBytes(got) {
						require.JSONEq(t, string(want), string(got), fixture)
					} else {
						require.Equal(t, string(want), string(got), fixture)
					}
				}
				require.Equal(t, original, body, fixture)
			}
		})
	}
}

func TestLegacyIngressNativeFastPathKeepsValidation(t *testing.T) {
	for _, body := range []string{`{"input":[]} trailing`, `{"input":[}`, `[{"input":[]}]`} {
		_, _, err := normalizeOpenAIResponsesLegacyIngress([]byte(body))
		require.Error(t, err)
	}
	body := []byte(`{"model":"gpt-5.4","input":"keep","mess\u0061ges":[]}`)
	out, changed, err := normalizeOpenAIResponsesLegacyIngress(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "messages").Exists())
	require.Equal(t, "keep", gjson.GetBytes(out, "input").String())
}

func BenchmarkOpenAILargeNativeRequest(b *testing.B) {
	body := largeNativeResponsesBody(69 << 20)
	for _, tc := range []struct {
		name string
		fn   func([]byte) ([]byte, bool, error)
	}{
		{"legacy_ingress", normalizeOpenAIResponsesLegacyIngress},
		{"input_ids", sanitizeOpenAIResponsesInputItemIDs},
		{"reasoning_replay", normalizeOpenAIResponsesReasoningContentReplay},
		{"api_key_store_false", func(body []byte) ([]byte, bool, error) {
			return normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, false)
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				out, changed, err := tc.fn(body)
				if err != nil || changed || !bytes.Equal(out, body) {
					b.Fatalf("native image request changed: changed=%v err=%v", changed, err)
				}
				runtime.KeepAlive(out)
			}
		})
	}
}
