//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyComputeGeminiTextDelta 是改造前的字符串拼接实现，作为只改存储方式的对照基准。
func legacyComputeGeminiTextDelta(seen, incoming string) (delta, newSeen string) {
	incoming = strings.TrimSuffix(incoming, "\u0000")
	if incoming == "" {
		return "", seen
	}
	if strings.HasPrefix(incoming, seen) {
		return strings.TrimPrefix(incoming, seen), incoming
	}
	if strings.HasPrefix(seen, incoming) {
		return "", seen
	}
	return incoming, seen + incoming
}

// 新实现与旧实现在累积、增量、重复/回退、空串与非 ASCII 场景下输出完全一致。
func TestGeminiTextDeltaMatchesLegacy(t *testing.T) {
	sequences := [][]string{
		{"Hel", "Hello", "Hello, wor", "Hello, world"},   // cumulative
		{"Hel", "lo", ", wor", "ld"},                     // incremental
		{"The", " cat", "The", " sat"},                   // delta equal to an emitted prefix
		{"abc", "abc", "ab", "abcd", "x"},                // duplicate, rewind, extend, diverge
		{"", "\u0000", "a\u0000", "ab", "", "abc\u0000"}, // empty and NUL-terminated chunks
		{"{\"a\":", "{\"a\":1", "}", "{\"a\":1}"},        // tool args mixing modes
		{"多字节", "多字节文本", "😀", "多字节文本😀继续"},                // non-ASCII
	}
	for _, seq := range sequences {
		var state geminiTextDelta
		legacySeen := ""
		for i, chunk := range seq {
			wantDelta, newSeen := legacyComputeGeminiTextDelta(legacySeen, chunk)
			legacySeen = newSeen
			require.Equal(t, wantDelta, state.next(chunk), "sequence %q chunk %d", seq, i)
			require.Equal(t, legacySeen, state.seen.String(), "sequence %q chunk %d", seq, i)
		}
		state.reset()
		require.Equal(t, "", state.seen.String())
		require.Equal(t, "fresh", state.next("fresh"))
	}
}

// 增量模式的长流分配次数保持线性以下。
func TestGeminiTextDeltaIncrementalAllocationsStayLinear(t *testing.T) {
	chunk := strings.Repeat("x", 100)
	const chunks = 2000
	allocs := testing.AllocsPerRun(5, func() {
		var state geminiTextDelta
		for i := 0; i < chunks; i++ {
			_ = state.next(chunk)
		}
	})
	// 字符串拼接每个 chunk 都分配一次；均摊增长只需 O(log n) 次。
	require.Less(t, allocs, float64(100), "allocs per stream: %v", allocs)
}
