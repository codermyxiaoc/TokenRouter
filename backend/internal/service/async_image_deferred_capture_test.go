package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// 捕获、释放各自只在成功后完成；失败保持待结算可重试；扣费与释放互斥；同步请求没有容器时保持原路径。
func TestAsyncImageDeferredCaptureRetryableAndExclusive(t *testing.T) {
	require.Nil(t, AsyncImageDeferredCaptureFromContext(context.Background()))

	ctx, holder := WithAsyncImageDeferredCapture(context.Background())
	require.Same(t, holder, AsyncImageDeferredCaptureFromContext(ctx))
	require.False(t, holder.Pending())

	captures, releases := 0, 0
	failCapture := true
	capture := func(context.Context) error {
		captures++
		if failCapture {
			return errors.New("database unavailable")
		}
		return nil
	}
	release := func(context.Context) error { releases++; return nil }
	require.True(t, holder.Defer(capture, release))
	require.False(t, holder.Defer(capture, release), "账号重试不能覆盖首次登记")

	// 回调失败不能消耗结算机会。
	require.Error(t, holder.Capture(context.Background()))
	require.True(t, holder.Pending(), "失败的捕获必须保持可重试")
	require.False(t, holder.Captured())
	failCapture = false
	require.NoError(t, holder.Capture(context.Background()))
	require.True(t, holder.Captured())
	require.False(t, holder.Pending())
	require.NoError(t, holder.Capture(context.Background()), "成功后幂等")
	require.Equal(t, 2, captures)
	require.ErrorIs(t, holder.Release(context.Background()), ErrAsyncImageAlreadyCaptured, "已扣费不能再释放")
	require.Zero(t, releases)

	// 释放失败同样可重试；释放后不能再扣费。
	_, undelivered := WithAsyncImageDeferredCapture(context.Background())
	failRelease := true
	require.True(t, undelivered.Defer(capture, func(context.Context) error {
		releases++
		if failRelease {
			return errors.New("ledger down")
		}
		return nil
	}))
	require.Error(t, undelivered.Release(context.Background()))
	require.True(t, undelivered.Pending())
	failRelease = false
	require.NoError(t, undelivered.Release(context.Background()))
	require.False(t, undelivered.Pending())
	require.Equal(t, 2, releases)
	require.Error(t, undelivered.Capture(context.Background()), "已释放不能扣费")
	require.Equal(t, 2, captures, "未交付不得捕获")

	// 无预占的后结算没有释放动作，释放直接视为完成。
	_, noRelease := WithAsyncImageDeferredCapture(context.Background())
	require.True(t, noRelease.Defer(capture, nil))
	require.NoError(t, noRelease.Release(context.Background()))
	require.False(t, noRelease.Pending())
	require.Equal(t, 2, captures)
}
