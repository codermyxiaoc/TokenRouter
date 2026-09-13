//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCanTransitionCreativeRun 校验创作台任务状态机。
func TestCanTransitionCreativeRun(t *testing.T) {
	valid := []struct{ from, to string }{
		{CreativeRunStatusQueued, CreativeRunStatusRunning},
		{CreativeRunStatusQueued, CreativeRunStatusCancelled},
		// 创建失败回滚路径允许 queued 直接转 failed。
		{CreativeRunStatusQueued, CreativeRunStatusFailed},
		// worker 恢复发现载荷过期时允许 queued 直接转 result_lost。
		{CreativeRunStatusQueued, CreativeRunStatusResultLost},
		{CreativeRunStatusRunning, CreativeRunStatusSucceeded},
		{CreativeRunStatusRunning, CreativeRunStatusFailed},
		{CreativeRunStatusRunning, CreativeRunStatusCancelled},
		{CreativeRunStatusRunning, CreativeRunStatusResultLost},
		// 成功任务的临时输出过期后可降级为 result_lost。
		{CreativeRunStatusSucceeded, CreativeRunStatusResultLost},
	}
	for _, tc := range valid {
		require.True(t, CanTransitionCreativeRun(tc.from, tc.to), "%s -> %s 应当合法", tc.from, tc.to)
	}

	invalid := []struct{ from, to string }{
		{"", CreativeRunStatusRunning},
		{CreativeRunStatusQueued, ""},
		{CreativeRunStatusQueued, CreativeRunStatusSucceeded},
		{CreativeRunStatusRunning, CreativeRunStatusQueued},
		{CreativeRunStatusRunning, CreativeRunStatusRunning},
		{CreativeRunStatusSucceeded, CreativeRunStatusRunning},
		{CreativeRunStatusFailed, CreativeRunStatusRunning},
		{CreativeRunStatusCancelled, CreativeRunStatusRunning},
		{CreativeRunStatusResultLost, CreativeRunStatusRunning},
		{CreativeRunStatusSucceeded, CreativeRunStatusFailed},
		{CreativeRunStatusFailed, CreativeRunStatusResultLost},
	}
	for _, tc := range invalid {
		require.False(t, CanTransitionCreativeRun(tc.from, tc.to), "%s -> %s 应当非法", tc.from, tc.to)
	}
}

func TestIsTerminalCreativeRunStatus(t *testing.T) {
	for _, status := range []string{CreativeRunStatusSucceeded, CreativeRunStatusFailed, CreativeRunStatusCancelled, CreativeRunStatusResultLost} {
		require.True(t, IsTerminalCreativeRunStatus(status))
	}
	for _, status := range []string{CreativeRunStatusQueued, CreativeRunStatusRunning, ""} {
		require.False(t, IsTerminalCreativeRunStatus(status))
	}
}

func TestIsValidCreativeRunID(t *testing.T) {
	require.True(t, IsValidCreativeRunID("crun_0123456789abcdef"))
	require.False(t, IsValidCreativeRunID("imgbatch_0123"))
	require.False(t, IsValidCreativeRunID("crun_"))
	require.False(t, IsValidCreativeRunID(""))
}

func TestNewCreativeRunID(t *testing.T) {
	runID, err := NewCreativeRunID()
	require.NoError(t, err)
	require.True(t, IsValidCreativeRunID(runID))
	other, err := NewCreativeRunID()
	require.NoError(t, err)
	require.NotEqual(t, runID, other)
}

// TestBuildCreativeRequestFingerprint 校验指纹确定性：输入不变指纹不变，任一字段变化指纹变化。
func TestBuildCreativeRequestFingerprint(t *testing.T) {
	base := creativeFingerprintPayload{
		GroupID:      12,
		Model:        "gemini-3.1-flash-image",
		Operation:    CreativeOperationGenerate,
		PromptSHA256: sha256Hex([]byte("hello")),
		ImageSHA256:  []string{sha256Hex([]byte("img"))},
		ImageSize:    "1K",
		AspectRatio:  "1:1",
		OutputCount:  1,
	}
	first := buildCreativeRequestFingerprint(base)
	require.NotEmpty(t, first)
	require.Equal(t, first, buildCreativeRequestFingerprint(base))

	changedPrompt := base
	changedPrompt.PromptSHA256 = sha256Hex([]byte("world"))
	require.NotEqual(t, first, buildCreativeRequestFingerprint(changedPrompt))

	changedGroup := base
	changedGroup.GroupID = 13
	require.NotEqual(t, first, buildCreativeRequestFingerprint(changedGroup))
}
