//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 创作台素材保存期：缺键沿用升级前的七天；读写错误和非法值也回退七天，绝不写成零或负值。
func TestGetCreativeCanvasAssetRetention(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		err   error
		want  time.Duration
	}{
		{name: "missing", err: ErrSettingNotFound, want: 168 * time.Hour},
		{name: "minimum", value: "1", want: time.Hour},
		{name: "custom", value: "72", want: 72 * time.Hour},
		{name: "maximum", value: "8760", want: 8760 * time.Hour},
		{name: "trimmed", value: " 48 ", want: 48 * time.Hour},
		{name: "zero", value: "0", want: 168 * time.Hour},
		{name: "negative", value: "-5", want: 168 * time.Hour},
		{name: "garbage", value: "abc", want: 168 * time.Hour},
		{name: "over_max", value: "8761", want: 168 * time.Hour},
		{name: "read_error", err: errors.New("database unavailable"), want: 168 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &SettingService{settingRepo: &mediaReconciliationSettingRepoStub{value: tc.value, err: tc.err}}
			require.Equal(t, tc.want, svc.GetCreativeCanvasAssetRetention(context.Background()))
		})
	}
	// 未注入仓储时同样回退默认值，不能让素材永不过期。
	require.Equal(t, 168*time.Hour, (&SettingService{}).GetCreativeCanvasAssetRetention(context.Background()))
	require.Error(t, ValidateCreativeCanvasAssetRetentionHours(0))
	require.NoError(t, ValidateCreativeCanvasAssetRetentionHours(8760))
}

// 画布归档使用配置的保存期；设置读取接口缺失时沿用原七天默认。
func TestCreativeCanvasAssetRetentionAppliedOnArchive(t *testing.T) {
	reader := &creativeFakeSettingReader{enabled: true, canvasRetention: 3 * time.Hour}
	creative := &CreativePublicService{Settings: reader}
	svc := NewCreativeCanvasService(nil, nil, creative)
	require.Equal(t, 3*time.Hour, svc.assetRetention(context.Background()))
	svc.creative = &CreativePublicService{}
	require.Equal(t, CreativeCanvasAssetTTL, svc.assetRetention(context.Background()))
	require.Equal(t, CreativeCanvasAssetTTL, (&CreativeCanvasService{}).assetRetention(context.Background()))
}
