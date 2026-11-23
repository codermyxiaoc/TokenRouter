package typesafe

import (
	"encoding/json"
	"strings"
	"testing"
)

// 模糊测试只生成本地字节串；成功解析必须返回请求内的非空模型且保持完整 JSON。
func FuzzSystemOneAdversarialRequest(f *testing.F) {
	for _, body := range []string{
		`{"model":"jev-latest","state":"ok","questions":{"q":{"type":"noul"}}}`,
		`{"model":"jev-latest","model":"jev-1.13-free","state":{},"questions":{"q":{"type":"choice","criteria":{}}}}`,
		`{"Model":"jev-latest","state":[],"questions":{"q":{"type":"score","criteria":[{}]}},"stream":false}`,
		`{"model":"jev-latest","state":{"nested":[[[]]]},"questions":{"q":{"type":"noul","criteria":{"true":null}}}}`,
		`null`, `[]`, `{"stream":"true"}`, `{"state":"\ud800"}`,
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		model, err := ValidateSystemOneRequest(body)
		if err != nil {
			return
		}
		if !json.Valid(body) {
			t.Fatalf("接受非法 JSON：%q", body)
		}
		var request struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &request) != nil || strings.TrimSpace(model) == "" || model != request.Model {
			t.Fatalf("解析型号与请求不一致：%q", model)
		}
	})
}
