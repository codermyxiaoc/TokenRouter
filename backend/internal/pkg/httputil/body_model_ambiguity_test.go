package httputil

import (
	"bytes"
	"compress/gzip"
	"errors"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/jsonutil"
	"github.com/klauspost/compress/zstd"
)

// 各种传输编码必须在返回路由层之前拒绝歧义模型，不能被解压或宽容规范化绕过。
func TestRequestBodyRejectsAmbiguousRoutingModel(t *testing.T) {
	for _, body := range []string{
		`{"model":"allowed","model":"blocked"}`,
		`{"model":"allowed","\u006dodel":"blocked"}`,
		`{"Model":"blocked","model":"allowed"}`,
		`{"model":"allowed","previous_response_id":null,"previous_response_id":"resp_foreign"}`,
		`{"model":"allowed","prompt_cache_key":"one","Prompt_Cache_Key":"two"}`,
	} {
		for _, encoding := range []string{"", "gzip", "zstd"} {
			t.Run(encoding+body, func(t *testing.T) {
				payload := []byte(body)
				switch encoding {
				case "gzip":
					var b bytes.Buffer
					w := gzip.NewWriter(&b)
					_, err := w.Write(payload)
					if err != nil {
						t.Fatal(err)
					}
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
					payload = b.Bytes()
				case "zstd":
					w, err := zstd.NewWriter(nil)
					if err != nil {
						t.Fatal(err)
					}
					payload = w.EncodeAll(payload, nil)
					w.Close()
				}
				got, err := ReadRequestBodyWithPrealloc(newRequestWithBody(t, payload, encoding))
				if !errors.Is(err, jsonutil.ErrDuplicateField) || got != nil {
					t.Fatalf("ambiguous body escaped validation: body=%s err=%v", got, err)
				}
			})
		}
	}
	// 真实换行会使原始 JSON 非法，必须在宽容解析修复后再次检查。
	body := []byte("{\"model\":\"allowed\",\"prompt\":\"line\nline\",\"model\":\"blocked\"}")
	if got, err := NormalizeLenientJSONRequestBody(body, 4096); !errors.Is(err, jsonutil.ErrDuplicateField) || got != nil {
		t.Fatalf("lenient normalization bypass: body=%s err=%v", got, err)
	}
}
