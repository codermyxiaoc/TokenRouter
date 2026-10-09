package jsonutil

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

// ErrDuplicateField 表示不同 JSON 解析器可能对同一字段取不同值，不能继续选路或计量。
var ErrDuplicateField = errors.New("duplicate JSON field")

// ValidateUniqueFields 校验顶层对象中指定字段的唯一性；未指定字段时检查全部顶层字段。
// 字段名先解码再忽略大小写比较，与 encoding/json 的结构体匹配保持一致；不改变嵌套供应商参数。
func ValidateUniqueFields(body []byte, fields ...string) error {
	if !gjson.ValidBytes(body) {
		return errors.New("expected a valid JSON object")
	}
	object := gjson.ParseBytes(body)
	if !object.IsObject() {
		return errors.New("expected a valid JSON object")
	}
	return validateUniqueObjectFields(object, fields)
}

func validateUniqueObjectFields(object gjson.Result, fields []string) error {
	seen := make(map[string]struct{})
	var err error
	object.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		canonical := name
		selected := len(fields) == 0
		for _, field := range fields {
			if strings.EqualFold(name, field) {
				selected = true
				// 用命中的规范字段名判重，覆盖长 s 等 Unicode 简单大小写折叠。
				canonical = field
				break
			}
		}
		if !selected {
			return true
		}
		if len(fields) == 0 {
			canonical = foldJSONFieldName(name)
		}
		if _, exists := seen[canonical]; exists {
			err = fmt.Errorf("%w: %s", ErrDuplicateField, name)
			return false
		}
		seen[canonical] = struct{}{}
		return true
	})
	return err
}

// ValidateRoutingModel 拒绝模型和显式会话标识歧义，保留非 JSON、空体及非法 JSON 的原有端点错误语义。
// 原生音视频的二进制或 multipart 内容不进入 JSON 扫描；宽容 JSON 在规范化后再次检查。
func ValidateRoutingModel(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !gjson.ValidBytes(trimmed) {
		return nil
	}
	return validateUniqueObjectFields(gjson.ParseBytes(trimmed), []string{"model", "previous_response_id", "prompt_cache_key"})
}

// 全字段判重与 encoding/json 的 EqualFold 等价类一致，不使用会漏掉长 s/Kelvin 符号的简单小写转换。
func foldJSONFieldName(name string) string {
	return strings.Map(func(r rune) rune {
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		return canonical
	}, name)
}
