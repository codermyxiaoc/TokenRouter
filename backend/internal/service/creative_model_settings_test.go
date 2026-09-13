//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// NormalizeCreativeModelSettings 覆盖白名单字段的校验、去重与稳定排序。
func TestNormalizeCreativeModelSettings(t *testing.T) {
	settings, err := NormalizeCreativeModelSettings([]CreativeModelSetting{{
		GroupID:    12,
		Model:      " gemini-3.1-flash-image ",
		Operations: []string{"inpaint", "generate", "generate"},
	}})
	require.NoError(t, err)
	require.Equal(t, []CreativeModelSetting{{
		GroupID:    12,
		Model:      "gemini-3.1-flash-image",
		Operations: []string{"generate", "inpaint"},
	}}, settings)

	for name, input := range map[string][]CreativeModelSetting{
		"非正整数分组": {{GroupID: 0, Model: "image", Operations: []string{CreativeOperationGenerate}}},
		"空模型":    {{GroupID: 1, Model: " ", Operations: []string{CreativeOperationGenerate}}},
		"空能力":    {{GroupID: 1, Model: "image", Operations: nil}},
		"非法能力":   {{GroupID: 1, Model: "image", Operations: []string{"upscale"}}},
		"重复模型": {
			{GroupID: 1, Model: "image", Operations: []string{CreativeOperationGenerate}},
			{GroupID: 1, Model: "image", Operations: []string{CreativeOperationEdit}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeCreativeModelSettings(input)
			require.Error(t, err)
		})
	}
}

func TestParseCreativeModelSettingsFailsClosed(t *testing.T) {
	require.Empty(t, parseCreativeModelSettings("{broken"))
	require.Empty(t, parseCreativeModelSettings(`[{"group_id":1,"model":"image","operations":[]}]`))

	parsed := parseCreativeModelSettings(`[{
		"group_id": 7,
		"model": "gpt-image-2",
		"operations": ["edit", "generate"]
	}]`)
	require.Equal(t, []CreativeModelSetting{{
		GroupID:    7,
		Model:      "gpt-image-2",
		Operations: []string{"generate", "edit"},
	}}, parsed)
}

func TestCreativeOperationsForModelIntersectsPlatformSupport(t *testing.T) {
	index := creativeModelSettingsIndex([]CreativeModelSetting{{
		GroupID:    9,
		Model:      "grok-imagine",
		Operations: []string{"generate", "edit"},
	}})
	operations, configured := creativeOperationsForModel(index, 9, "grok-imagine", []string{CreativeOperationGenerate})
	require.True(t, configured)
	require.Equal(t, []string{CreativeOperationGenerate}, operations)

	operations, configured = creativeOperationsForModel(index, 10, "grok-imagine", []string{CreativeOperationGenerate})
	require.False(t, configured)
	require.Empty(t, operations)
}

// TestNormalizeCreativeModelSettingsForSaveByPlatform 校验保存时按实际平台清理能力。
func TestNormalizeCreativeModelSettingsForSaveByPlatform(t *testing.T) {
	svc := newCreativeTestService()
	groupRepo := svc.GroupRepo.(*creativeFakeGroupRepo)
	openai := newCreativeTestGroup()
	openai.ID = 13
	openai.Name = "OpenAI Image"
	openai.Platform = PlatformOpenAI
	groupRepo.byID[13] = openai

	got, err := svc.NormalizeCreativeModelSettingsForSave(context.Background(), []CreativeModelSetting{
		{GroupID: 12, Model: "gemini-3.1-flash-image", Operations: []string{CreativeOperationGenerate, CreativeOperationInpaint}},
		{GroupID: 13, Model: "gpt-image-2", Operations: []string{CreativeOperationGenerate, CreativeOperationInpaint}},
		{GroupID: 12, Model: "gemini-only-inpaint", Operations: []string{CreativeOperationInpaint}},
		{GroupID: 999, Model: "legacy", Operations: []string{CreativeOperationInpaint}},
	})
	require.NoError(t, err)
	require.Equal(t, []CreativeModelSetting{
		{GroupID: 12, Model: "gemini-3.1-flash-image", Operations: []string{CreativeOperationGenerate}},
		{GroupID: 13, Model: "gpt-image-2", Operations: []string{CreativeOperationGenerate, CreativeOperationInpaint}},
		{GroupID: 999, Model: "legacy", Operations: []string{CreativeOperationInpaint}},
	}, got)
}

func TestSettingServiceGetCreativeModelSettings(t *testing.T) {
	repo := &settingUpdateRepoStub{values: map[string]string{
		SettingKeyCreativeModelSettings: `[{"group_id":12,"model":"gpt-image-2","operations":["generate"]}]`,
	}}
	svc := NewSettingService(repo, nil)
	require.Equal(t, []CreativeModelSetting{{
		GroupID:    12,
		Model:      "gpt-image-2",
		Operations: []string{CreativeOperationGenerate},
	}}, svc.GetCreativeModelSettings(context.Background()))

	repo.values[SettingKeyCreativeModelSettings] = "not-json"
	require.Empty(t, svc.GetCreativeModelSettings(context.Background()))
}

func TestBuildSystemSettingsUpdatesCreativeModelSettings(t *testing.T) {
	svc := NewSettingService(&settingUpdateRepoStub{}, nil)
	updates, err := svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{
		CreativeModelSettings: []CreativeModelSetting{{
			GroupID:    3,
			Model:      "gpt-image-2",
			Operations: []string{CreativeOperationInpaint, CreativeOperationGenerate},
		}},
	})
	require.NoError(t, err)
	var persisted []CreativeModelSetting
	require.NoError(t, json.Unmarshal([]byte(updates[SettingKeyCreativeModelSettings]), &persisted))
	require.Equal(t, []CreativeModelSetting{{
		GroupID:    3,
		Model:      "gpt-image-2",
		Operations: []string{CreativeOperationGenerate, CreativeOperationInpaint},
	}}, persisted)

	_, err = svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{
		CreativeModelSettings: []CreativeModelSetting{{GroupID: 3, Model: "gpt-image-2"}},
	})
	require.Error(t, err)
}

func TestCreativeModelSettingsEmptyListClosesCreativeDirectory(t *testing.T) {
	svc := newCreativeTestService()
	svc.Settings = &creativeFakeSettingReader{enabled: true, models: []CreativeModelSetting{}}
	models, err := svc.ListModels(context.Background(), 7)
	require.NoError(t, err)
	require.Empty(t, models.Data)
}
