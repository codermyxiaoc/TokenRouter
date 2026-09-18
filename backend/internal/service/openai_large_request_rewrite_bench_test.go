package service

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"
)

func BenchmarkOpenAIStoreFalseMetadataRewrite(b *testing.B) {
	body := largeNativeResponsesBody(69 << 20)
	body = bytes.Replace(body, []byte(`"input":[`), []byte(`"input":[{"type":"reasoning","id":"rs_server_id","encrypted_content":"opaque","summary":[]},`), 1)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, changed, err := normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, false)
		if err != nil || !changed || len(out) < 69<<20 {
			b.Fatalf("metadata rewrite failed or image was lost: changed=%v err=%v", changed, err)
		}
		runtime.KeepAlive(out)
	}
}

// BenchmarkOpenAIResponsesMemoryComparison 在同一进程比较旧实现与局部优化，避免跨版本环境偏差。
func BenchmarkOpenAIResponsesMemoryComparison(b *testing.B) {
	for _, sizeMiB := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("%dMiB", sizeMiB), func(b *testing.B) {
			native := largeNativeResponsesBody(sizeMiB << 20)
			rewrite := bytes.Replace(native, []byte(`"input":[`), []byte(`"input":[{"type":"reasoning","id":"rs_metadata","encrypted_content":"opaque","summary":[]},`), 1)
			for _, scenario := range []struct {
				name          string
				body          []byte
				changed       bool
				before, after func([]byte) ([]byte, bool, error)
			}{
				{"legacy_noop", native, false, memoryBaselineLegacyIngress, normalizeOpenAIResponsesLegacyIngress},
				{"ids_noop", native, false, memoryBaselineInputItemIDs, sanitizeOpenAIResponsesInputItemIDs},
				{"ids_rewrite", bytes.Replace(native, []byte(`"input":[`), []byte(`"input":[{"type":"message","id":"bad","content":"metadata"},`), 1), true, memoryBaselineInputItemIDs, sanitizeOpenAIResponsesInputItemIDs},
				{"reasoning_noop", native, false, memoryBaselineReasoningContent, normalizeOpenAIResponsesReasoningContentReplay},
				{"store_false_noop", native, false, func(body []byte) ([]byte, bool, error) {
					return normalizeOpenAIAPIKeyStoreFalseReasoningReplayDecoded(body, false)
				}, func(body []byte) ([]byte, bool, error) {
					return normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, false)
				}},
				{"store_false_rewrite", rewrite, true, func(body []byte) ([]byte, bool, error) {
					return normalizeOpenAIAPIKeyStoreFalseReasoningReplayDecoded(body, false)
				}, func(body []byte) ([]byte, bool, error) {
					return normalizeOpenAIAPIKeyStoreFalseReasoningReplay(body, false)
				}},
			} {
				b.Run(scenario.name, func(b *testing.B) {
					for _, impl := range []struct {
						name string
						fn   func([]byte) ([]byte, bool, error)
					}{{"before", scenario.before}, {"after", scenario.after}} {
						b.Run(impl.name, func(b *testing.B) {
							b.ReportAllocs()
							b.SetBytes(int64(len(scenario.body)))
							b.ResetTimer()
							for i := 0; i < b.N; i++ {
								out, changed, err := impl.fn(scenario.body)
								if err != nil || changed != scenario.changed {
									b.Fatalf("unexpected result: changed=%v err=%v", changed, err)
								}
								runtime.KeepAlive(out)
							}
						})
					}
				})
			}
		})
	}
}
