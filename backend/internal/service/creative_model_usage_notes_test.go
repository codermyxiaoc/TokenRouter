//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func creativeUsageNotesVideoSetting() CreativeModelSetting {
	return CreativeModelSetting{GroupID: 3, Model: "public-video-alias", MediaType: "video", VideoEndpoint: VideoEndpointCompat, Operations: []string{CreativeOperationGenerate}}
}

// 配置保存、管理端读取及运行目录读取使用同一 JSON，换行和主动清空不能丢失。
func TestCreativeModelUsageNotesSettingsRoundTripAndClear(t *testing.T) {
	ctx := context.Background()
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, nil)
	setting := creativeUsageNotesVideoSetting()
	setting.UsageNotes = " \r\n首帧请选择自动比例。\r\n\t支持中文与 🎬。\r说明只作提示。 \n"
	updates, err := svc.buildSystemSettingsUpdates(ctx, &SystemSettings{CreativeModelSettings: []CreativeModelSetting{setting}})
	require.NoError(t, err)
	require.NoError(t, repo.SetMultiple(ctx, updates))
	got := svc.GetCreativeModelSettings(ctx)
	require.Len(t, got, 1)
	require.Equal(t, "首帧请选择自动比例。\n\t支持中文与 🎬。\n说明只作提示。", got[0].UsageNotes)
	admin, err := svc.GetAllSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, got, admin.CreativeModelSettings)
	require.Equal(t, setting.UsageNotes, " \r\n首帧请选择自动比例。\r\n\t支持中文与 🎬。\r说明只作提示。 \n", "规范化不得改写调用方表单")

	for _, cleared := range []string{"", " \t\r\n "} {
		setting.UsageNotes = cleared
		updates, err = svc.buildSystemSettingsUpdates(ctx, &SystemSettings{CreativeModelSettings: []CreativeModelSetting{setting}})
		require.NoError(t, err)
		require.NotContains(t, updates[SettingKeyCreativeModelSettings], "usage_notes")
		require.NoError(t, repo.SetMultiple(ctx, updates))
		require.Empty(t, svc.GetCreativeModelSettings(ctx)[0].UsageNotes)
	}
	legacy := parseCreativeModelSettings(`[{"group_id":3,"model":"public-video-alias","media_type":"video","video_endpoint":"compat","operations":["generate"]}]`)
	require.Len(t, legacy, 1)
	require.Empty(t, legacy[0].UsageNotes)
}

// 中文与表情按码点计数；错误说明不能混入非视频配置或带入不可显示的控制字符。
func TestCreativeModelUsageNotesValidation(t *testing.T) {
	for _, notes := range []string{strings.Repeat("注", 2000), strings.Repeat("🎬", 2000), "<img src=x onerror=alert(1)>\n这是纯文本"} {
		setting := creativeUsageNotesVideoSetting()
		setting.UsageNotes = notes
		got, err := NormalizeCreativeModelSettings([]CreativeModelSetting{setting})
		require.NoError(t, err)
		require.Equal(t, notes, got[0].UsageNotes, "正文不应被 HTML 解析或字节数截断")
	}
	for name, notes := range map[string]string{
		"中文超长": strings.Repeat("注", 2001),
		"表情超长": strings.Repeat("🎬", 2001),
		"空字节":  "注意\x00事项",
		"终端控制": "说明\x1b[31m",
		"删除控制": "说明\x7f",
		"非法编码": string([]byte{0xff, 0xfe}),
	} {
		t.Run(name, func(t *testing.T) {
			setting := creativeUsageNotesVideoSetting()
			setting.UsageNotes = notes
			_, err := NormalizeCreativeModelSettings([]CreativeModelSetting{setting})
			require.ErrorContains(t, err, "usage_notes")
		})
	}
	for _, mediaType := range []string{"", "image", "text", "audio"} {
		setting := CreativeModelSetting{GroupID: 3, Model: "model", MediaType: mediaType, Operations: []string{CreativeOperationGenerate}, UsageNotes: "视频说明"}
		if mediaType == "audio" {
			setting.Model = CreativeGrokTTSModel
		}
		_, err := NormalizeCreativeModelSettings([]CreativeModelSetting{setting})
		require.ErrorContains(t, err, "usage_notes")
	}
	require.Empty(t, parseCreativeModelSettings(`[{"group_id":3,"model":"video","media_type":"video","video_endpoint":"compat","operations":["generate"],"usage_notes":{"html":"invalid"}}]`))
}

// 同名模型的说明按分组隔离，沿用目录原有权限；关闭或清空配置立即停止公开旧说明。
func TestCreativeModelUsageNotesPublicVideoAndGrokIsolation(t *testing.T) {
	for _, grok := range []bool{false, true} {
		name := "Video"
		if grok {
			name = "Grok"
		}
		t.Run(name, func(t *testing.T) {
			executor, execution, _, _, _ := creativeVideoTestFixture(t)
			svc, userID := executor.public, execution.UserID
			if grok {
				svc, _ = newCreativeGrokVideoCatalogTestService()
				userID = 7
			}
			reader := svc.Settings.(*creativeFakeSettingReader)
			configured := reader.models[0]
			configured.UsageNotes = "本分组：首帧请选自动。\n<img src=x onerror=alert(1)>"
			otherGroup := configured
			otherGroup.GroupID = 999
			otherGroup.UsageNotes = "其它分组的说明不可泄漏"
			reader.models = []CreativeModelSetting{otherGroup, configured}
			models, err := svc.ListModels(context.Background(), userID)
			require.NoError(t, err)
			require.Len(t, models.Data, 1)
			require.Equal(t, configured.UsageNotes, models.Data[0].UsageNotes)
			raw, err := json.Marshal(models)
			require.NoError(t, err)
			require.NotContains(t, string(raw), otherGroup.UsageNotes)
			require.NotContains(t, string(raw), "<img", "目录的标准 JSON 输出应继续转义 HTML 字符")
			candidates, err := svc.ListCreativeModelCandidates(context.Background())
			require.NoError(t, err)
			rawCandidates, err := json.Marshal(candidates)
			require.NoError(t, err)
			require.NotContains(t, string(rawCandidates), "usage_notes", "候选不能猜测供应商说明")

			svc.UserRepo.(*creativeFakeUserRepo).user.DisabledPublicGroups = []int64{configured.GroupID}
			models, err = svc.ListModels(context.Background(), userID)
			require.NoError(t, err)
			require.Empty(t, models.Data)
			svc.UserRepo.(*creativeFakeUserRepo).user.DisabledPublicGroups = nil
			reader.models[1].UsageNotes = ""
			models, err = svc.ListModels(context.Background(), userID)
			require.NoError(t, err)
			require.Len(t, models.Data, 1)
			raw, err = json.Marshal(models)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "usage_notes")
			reader.enabled = false
			models, err = svc.ListModels(context.Background(), userID)
			require.NoError(t, err)
			require.Empty(t, models.Data)
		})
	}
}

// 文案只能改变目录，不能混入供应商正文或改写原视频参数与计费维度。
func TestCreativeModelUsageNotesDoNotChangeVideoRequest(t *testing.T) {
	executor, execution, _, _, _ := creativeVideoTestFixture(t)
	models, err := executor.public.ListModels(context.Background(), execution.UserID)
	require.NoError(t, err)
	require.Len(t, models.Data, 1)
	model := models.Data[0]
	before, _, err := creativeVideoCanvasRequest(execution, &model, nil, 8000)
	require.NoError(t, err)
	model.UsageNotes = "首帧比例由参考图决定；这段说明不能进入提示词。"
	after, _, err := creativeVideoCanvasRequest(execution, &model, nil, 8000)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NotContains(t, string(after), "usage_notes")
	require.NotContains(t, string(after), model.UsageNotes)
}
