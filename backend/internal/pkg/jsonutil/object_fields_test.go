package jsonutil

import (
	"errors"
	"testing"
)

// 不同解析器的大小写及转义差异必须在改写模型之前拒绝，嵌套供应商字段保持原样。
func TestRoutingModelRejectsAmbiguousNames(t *testing.T) {
	for _, body := range []string{
		`{"model":"one","model":"two"}`,
		`{"model":"one","Model":"two"}`,
		`{"model":"one","\u006dodel":"two"}`,
		`{"MODEL":"one","mOdEl":"two"}`,
		`{"model":"one","previous_response_id":null,"previous_response_id":"resp_foreign"}`,
		`{"prompt_cache_key":"one","Prompt_Cache_Key":"two"}`,
		`{"prompt_cache_key":"one","\u0070rompt_cache_key":"two"}`,
		`{"previous_response_id":null,"previous_reſponse_id":"resp_foreign"}`,
		`{"prompt_cache_key":"one","prompt_cache_Key":"two"}`,
	} {
		if err := ValidateRoutingModel([]byte(body)); !errors.Is(err, ErrDuplicateField) {
			t.Errorf("未拒绝歧义模型 %s: %v", body, err)
		}
	}
	for _, body := range []string{`{"model":"one","tools":[{"model":"two"}]}`, `{"model":"one","metadata":{"model":"nested"}}`, `{"model":"one","vendor":1,"vendor":2}`, ``, `binary body`, `[1,2]`, `{"model":`} {
		if err := ValidateRoutingModel([]byte(body)); err != nil {
			t.Errorf("改变了其它端点的原有校验语义 %s: %v", body, err)
		}
	}
}

func TestUniqueFieldsRejectsAmbiguousUsage(t *testing.T) {
	for _, body := range []string{`{"input_tokens":0,"input_tokens":100}`, `{"usage":{},"Usage":{}}`, `[]`, `null`, `{"usage":}`} {
		if err := ValidateUniqueFields([]byte(body)); err == nil {
			t.Errorf("未拒绝无效或歧义对象 %s", body)
		}
	}
	if err := ValidateUniqueFields([]byte(`{"usage":{"input_tokens":1},"other":1,"other":2}`), "usage"); err != nil {
		t.Fatal(err)
	}
}

// 标准结构体解码会把长 s 和 Kelvin 符号归入同一字段，指定字段与全字段模式都必须拒绝。
func TestUniqueFieldsRejectsUnicodeFoldAliases(t *testing.T) {
	for _, body := range []string{`{"session":{},"ſession":{}}`, `{"tokens":1,"toKens":2}`, `{"usage":{},"u\u017fage":{}}`} {
		if err := ValidateUniqueFields([]byte(body)); !errors.Is(err, ErrDuplicateField) {
			t.Fatalf("Unicode alias accepted: %s: %v", body, err)
		}
	}
	if err := ValidateUniqueFields([]byte(`{"session":{},"ſession":{}}`), "session"); !errors.Is(err, ErrDuplicateField) {
		t.Fatal(err)
	}
	if err := ValidateUniqueFields([]byte(`{"σ":1,"ς":2}`)); !errors.Is(err, ErrDuplicateField) {
		t.Fatal(err)
	}
}
