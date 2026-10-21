//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

type mediaReconciliationSettingRepoStub struct {
	SettingRepository
	value string
	err   error
}

func (r *mediaReconciliationSettingRepoStub) GetValue(context.Context, string) (string, error) {
	return r.value, r.err
}

// 配置缺键允许升级默认，读取错误和非法值必须阻止自动释放，不能缩短人工核对时间。
func TestGetMediaTaskReconciliationTimeout(t *testing.T) {
	readErr := errors.New("database unavailable")
	for _, tc := range []struct {
		name  string
		value string
		err   error
		want  time.Duration
		fail  bool
	}{
		{name: "missing", err: ErrSettingNotFound, want: time.Hour},
		{name: "minimum", value: "1", want: time.Minute},
		{name: "maximum", value: "10080", want: 7 * 24 * time.Hour},
		{name: "trimmed", value: " 120 ", want: 2 * time.Hour},
		{name: "read_error", err: readErr, fail: true},
		{name: "empty_value", value: "", fail: true},
		{name: "zero", value: "0", fail: true},
		{name: "negative", value: "-1", fail: true},
		{name: "too_large", value: "10081", fail: true},
		{name: "fraction", value: "1.5", fail: true},
		{name: "invalid", value: "invalid", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewSettingService(&mediaReconciliationSettingRepoStub{value: tc.value, err: tc.err}, &config.Config{})
			got, err := svc.GetMediaTaskReconciliationTimeout(context.Background())
			if tc.fail {
				require.Error(t, err)
				require.Zero(t, got)
				if tc.err != nil {
					require.ErrorIs(t, err, tc.err)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	var nilService *SettingService
	_, err := nilService.GetMediaTaskReconciliationTimeout(context.Background())
	require.Error(t, err)
}

// 运行时每次读取最新值，管理员保存后无需重启，且默认展示不会回写历史配置。
func TestMediaTaskReconciliationTimeoutSettingsPersistence(t *testing.T) {
	repo := &settingUpdateRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	settings, err := svc.GetAllSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 60, settings.MediaTaskReconciliationTimeoutMinutes)
	require.Empty(t, repo.values)

	for _, minutes := range []int{1, 120, 10080} {
		require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{MediaTaskReconciliationTimeoutMinutes: minutes}))
		require.Equal(t, strconv.Itoa(minutes), repo.values[SettingKeyMediaTaskReconciliationTimeoutMinutes])
		got, err := svc.GetMediaTaskReconciliationTimeout(context.Background())
		require.NoError(t, err)
		require.Equal(t, time.Duration(minutes)*time.Minute, got)
	}
	for _, minutes := range []int{-1, 10081} {
		require.Error(t, svc.UpdateSettings(context.Background(), &SystemSettings{MediaTaskReconciliationTimeoutMinutes: minutes}))
		require.Equal(t, "10080", repo.values[SettingKeyMediaTaskReconciliationTimeoutMinutes])
	}
}
