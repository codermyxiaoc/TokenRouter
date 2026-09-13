package service

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/domain"
)

// bedrockModelRoute 同时保存推理 ID 和签名/请求端点使用的来源区域。
type bedrockModelRoute struct {
	ModelID      string
	SourceRegion string
}

type bedrockRoutingFailure string

const (
	bedrockRoutingInvalidModel      bedrockRoutingFailure = "invalid_model"
	bedrockRoutingUnsupportedRegion bedrockRoutingFailure = "unsupported_region"
	bedrockRoutingUnverifiedRegion  bedrockRoutingFailure = "unverified_region"
)

// bedrockModelRoutingError 对外只给出失败类别；账号配置细节由管理员诊断单独读取。
type bedrockModelRoutingError struct {
	Reason          bedrockRoutingFailure
	ModelID         string
	SourceRegion    string
	ForceGlobal     bool
	GlobalAvailable bool
}

func (e *bedrockModelRoutingError) Error() string {
	return "bedrock model routing failed: " + string(e.Reason)
}

// bedrockRoutingDiagnostic 仅用于管理员测试与内部日志，不向普通客户端暴露账号区域。
func bedrockRoutingDiagnostic(err error) string {
	var failure *bedrockModelRoutingError
	if !errors.As(err, &failure) {
		return err.Error()
	}
	mode := "地域推理"
	if failure.ForceGlobal {
		mode = "全局推理"
	}
	var message string
	switch failure.Reason {
	case bedrockRoutingInvalidModel:
		message = fmt.Sprintf("无法解析 Bedrock 模型 %q", failure.ModelID)
	case bedrockRoutingUnsupportedRegion:
		message = fmt.Sprintf("Bedrock 模型 %q 在来源区域 %q 不支持%s", failure.ModelID, failure.SourceRegion, mode)
	default:
		message = fmt.Sprintf("尚未核实 Bedrock 模型 %q 在来源区域 %q 的%s ID，请核对 AWS 推理配置", failure.ModelID, failure.SourceRegion, mode)
	}
	if !failure.ForceGlobal && failure.GlobalAvailable {
		message += "；该来源区域支持此模型的全局推理，可开启“强制全局”"
	}
	return message
}

// resolveBedrockModelRoute 只从已核实的模型/来源区域规则选择推理 ID，不按区域名称猜测。
// @project-doc docs/interfaces/anthropic_upstream.md#bedrock_region_routing
func resolveBedrockModelRoute(account *Account, requestedModel string) (bedrockModelRoute, error) {
	route := bedrockModelRoute{SourceRegion: bedrockRuntimeRegion(account)}
	if account == nil {
		return route, &bedrockModelRoutingError{Reason: bedrockRoutingInvalidModel, ModelID: requestedModel, SourceRegion: route.SourceRegion}
	}
	modelID := strings.TrimSpace(account.GetMappedModel(requestedModel))
	defaultID, isDefaultAlias := domain.DefaultBedrockModelMapping[modelID]
	if isDefaultAlias {
		modelID = defaultID
	}
	baseID := bedrockBaseModelID(modelID)
	rule, knownModel := bedrockModelRegionRules[baseID]
	failure := &bedrockModelRoutingError{
		ModelID: modelID, SourceRegion: route.SourceRegion, ForceGlobal: shouldForceBedrockGlobal(account),
	}
	if !knownModel {
		if !isDefaultAlias && isLikelyBedrockModelID(modelID) {
			// 完整未知 ID、其它厂商模型和 ARN 由上游解释，保留显式配置原样。
			route.ModelID = modelID
			return route, nil
		}
		failure.Reason = bedrockRoutingInvalidModel
		if isDefaultAlias {
			failure.Reason = bedrockRoutingUnverifiedRegion
		}
		return route, failure
	}
	failure.GlobalAvailable = rule.globalProfile.supports(route.SourceRegion)
	// 显式裸基础 ID 在已确认支持单区域调用的来源区域保持原样；默认地域预设仍按账号路由。
	if !failure.ForceGlobal && !isDefaultAlias && modelID == baseID && slices.Contains(rule.inRegionSources, route.SourceRegion) {
		route.ModelID = modelID
		return route, nil
	}
	if failure.ForceGlobal {
		if failure.GlobalAvailable {
			route.ModelID = rule.globalProfile.id
			return route, nil
		}
	} else {
		for _, profile := range rule.geoProfiles {
			if profile.supports(route.SourceRegion) {
				route.ModelID = profile.id
				return route, nil
			}
		}
	}
	// 文档缺少该区域或具体地域 ID 时，不能把“未核实”表述为 AWS 明确不支持。
	failure.Reason = bedrockRoutingUnverifiedRegion
	if slices.Contains(rule.documentedRegions, route.SourceRegion) &&
		(failure.ForceGlobal || !slices.Contains(rule.unverifiedGeoRegions, route.SourceRegion)) {
		failure.Reason = bedrockRoutingUnsupportedRegion
	}
	if failure.ForceGlobal && rule.globalProfile.id == "" && len(rule.documentedRegions) > 0 {
		failure.Reason = bedrockRoutingUnsupportedRegion
	}
	return route, failure
}

// bedrockBaseModelID 仅识别推理范围前缀，不剥离版本、日期或 ARN 的任何组成部分。
func bedrockBaseModelID(modelID string) string {
	for _, prefix := range bedrockCrossRegionPrefixes {
		if strings.HasPrefix(modelID, prefix) {
			return strings.TrimPrefix(modelID, prefix)
		}
	}
	return modelID
}

// bedrockInferenceProfile 的来源区域来自该精确 ID 的官方表，而非同系列模型的推断。
type bedrockInferenceProfile struct {
	id            string
	sourceRegions []string
}

func (p bedrockInferenceProfile) supports(region string) bool {
	return p.id != "" && slices.Contains(p.sourceRegions, region)
}

type bedrockModelRegionRule struct {
	sourceURL            string
	geoProfiles          []bedrockInferenceProfile
	globalProfile        bedrockInferenceProfile
	inRegionSources      []string
	documentedRegions    []string
	unverifiedGeoRegions []string
}

// bedrockProfile 仅在初始化规则表时拆分来源区域，请求路径不分配区域列表。
func bedrockProfile(id, sourceRegions string) bedrockInferenceProfile {
	return bedrockInferenceProfile{id: id, sourceRegions: strings.Fields(sourceRegions)}
}

// 以下来源区域集合仅用于复用完全相同的已核实列表，不代表任意型号均支持这些区域。
const (
	bedrockUSSources         = "us-east-1 us-east-2 us-west-1 us-west-2 ca-central-1 ca-west-1"
	bedrockEUSources         = "eu-central-1 eu-central-2 eu-north-1 eu-south-1 eu-south-2 eu-west-1 eu-west-2 eu-west-3"
	bedrockJPSources         = "ap-northeast-1 ap-northeast-3"
	bedrockAUSources         = "ap-southeast-2 ap-southeast-4"
	bedrockAUNZSources       = "ap-southeast-2 ap-southeast-4 ap-southeast-6"
	bedrockGovCloudSources   = "us-gov-east-1 us-gov-west-1"
	bedrockCommercialSources = "us-east-1 us-east-2 us-west-1 us-west-2 ca-central-1 ca-west-1 eu-central-1 eu-central-2 eu-north-1 eu-south-1 eu-south-2 eu-west-1 eu-west-2 eu-west-3 ap-east-2 ap-northeast-1 ap-northeast-2 ap-northeast-3 ap-south-1 ap-south-2 ap-southeast-1 ap-southeast-2 ap-southeast-3 ap-southeast-4 ap-southeast-5 ap-southeast-6 ap-southeast-7 il-central-1 me-central-1 me-south-1 af-south-1 sa-east-1 mx-central-1"
)

// bedrockModelRegionRules 于 2026-09-09 核对 AWS 模型详情页的地域 ID、来源区域及全局支持表。
// 概览声称支持地域推理、但未列出对应精确 ID/来源关系的条目保留为未核实；不拼接 us-gov 等前缀。
// sourceURL 记录每个型号自身的证据，升级时必须同时核对地域与全局来源区域。
var bedrockModelRegionRules = map[string]bedrockModelRegionRule{
	"anthropic.claude-opus-4-7": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-7.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-opus-4-7", bedrockUSSources),
			bedrockProfile("eu.anthropic.claude-opus-4-7", bedrockEUSources),
			bedrockProfile("jp.anthropic.claude-opus-4-7", bedrockJPSources),
			bedrockProfile("au.anthropic.claude-opus-4-7", bedrockAUSources),
		},
		globalProfile:     bedrockProfile("global.anthropic.claude-opus-4-7", bedrockCommercialSources),
		documentedRegions: strings.Fields(bedrockCommercialSources),
	},
	"anthropic.claude-opus-4-8": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-8.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-opus-4-8", bedrockUSSources),
			bedrockProfile("eu.anthropic.claude-opus-4-8", bedrockEUSources),
			bedrockProfile("jp.anthropic.claude-opus-4-8", bedrockJPSources),
			bedrockProfile("au.anthropic.claude-opus-4-8", bedrockAUSources),
		},
		globalProfile:        bedrockProfile("global.anthropic.claude-opus-4-8", bedrockCommercialSources),
		documentedRegions:    strings.Fields(bedrockCommercialSources + " " + bedrockGovCloudSources),
		unverifiedGeoRegions: strings.Fields(bedrockGovCloudSources),
	},
	"anthropic.claude-opus-5": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-5.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-opus-5", bedrockUSSources),
			bedrockProfile("eu.anthropic.claude-opus-5", bedrockEUSources),
			bedrockProfile("au.anthropic.claude-opus-5", bedrockAUSources),
		},
		globalProfile:        bedrockProfile("global.anthropic.claude-opus-5", bedrockCommercialSources),
		documentedRegions:    strings.Fields(bedrockCommercialSources + " " + bedrockGovCloudSources),
		unverifiedGeoRegions: strings.Fields(bedrockGovCloudSources),
	},
	"anthropic.claude-sonnet-5": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-5.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-sonnet-5", bedrockUSSources),
			bedrockProfile("eu.anthropic.claude-sonnet-5", bedrockEUSources),
			bedrockProfile("au.anthropic.claude-sonnet-5", bedrockAUSources),
		},
		globalProfile:        bedrockProfile("global.anthropic.claude-sonnet-5", bedrockCommercialSources),
		documentedRegions:    strings.Fields(bedrockCommercialSources + " " + bedrockGovCloudSources),
		unverifiedGeoRegions: strings.Fields(bedrockGovCloudSources),
	},
	"anthropic.claude-fable-5": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-fable-5.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-fable-5", bedrockUSSources),
		},
		globalProfile:     bedrockProfile("global.anthropic.claude-fable-5", bedrockCommercialSources),
		documentedRegions: strings.Fields(bedrockCommercialSources),
	},
	"anthropic.claude-fable-5-1": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-fable-5-1.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-fable-5-1", bedrockUSSources),
		},
		globalProfile:        bedrockProfile("global.anthropic.claude-fable-5-1", bedrockCommercialSources),
		documentedRegions:    strings.Fields(bedrockCommercialSources + " " + bedrockGovCloudSources),
		unverifiedGeoRegions: strings.Fields(bedrockGovCloudSources),
	},
	"anthropic.claude-opus-4-6-v1": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-6.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-opus-4-6-v1", bedrockUSSources),
			bedrockProfile("eu.anthropic.claude-opus-4-6-v1", bedrockEUSources),
			bedrockProfile("au.anthropic.claude-opus-4-6-v1", bedrockAUNZSources),
		},
		globalProfile:     bedrockProfile("global.anthropic.claude-opus-4-6-v1", bedrockCommercialSources),
		inRegionSources:   []string{"eu-west-2"},
		documentedRegions: strings.Fields(bedrockCommercialSources),
	},
	"anthropic.claude-sonnet-4-6": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-4-6.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-sonnet-4-6", bedrockUSSources),
			bedrockProfile("eu.anthropic.claude-sonnet-4-6", bedrockEUSources),
			bedrockProfile("au.anthropic.claude-sonnet-4-6", bedrockAUNZSources),
			bedrockProfile("jp.anthropic.claude-sonnet-4-6", bedrockJPSources),
		},
		globalProfile:     bedrockProfile("global.anthropic.claude-sonnet-4-6", bedrockCommercialSources),
		inRegionSources:   []string{"eu-west-2"},
		documentedRegions: strings.Fields(bedrockCommercialSources),
	},
	"anthropic.claude-opus-4-5-20251101-v1:0": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-5.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-opus-4-5-20251101-v1:0", "us-east-1 us-east-2 us-west-1 us-west-2 ca-central-1"),
			bedrockProfile("eu.anthropic.claude-opus-4-5-20251101-v1:0", bedrockEUSources),
		},
		globalProfile:     bedrockProfile("global.anthropic.claude-opus-4-5-20251101-v1:0", bedrockCommercialSources),
		documentedRegions: strings.Fields(bedrockCommercialSources),
	},
	"anthropic.claude-sonnet-4-5-20250929-v1:0": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-4-5.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-sonnet-4-5-20250929-v1:0", "us-east-1 us-east-2 us-west-1 us-west-2 us-gov-east-1 us-gov-west-1 ca-central-1"),
			bedrockProfile("eu.anthropic.claude-sonnet-4-5-20250929-v1:0", bedrockEUSources),
			bedrockProfile("au.anthropic.claude-sonnet-4-5-20250929-v1:0", bedrockAUNZSources),
			bedrockProfile("jp.anthropic.claude-sonnet-4-5-20250929-v1:0", bedrockJPSources),
		},
		globalProfile:     bedrockProfile("global.anthropic.claude-sonnet-4-5-20250929-v1:0", bedrockCommercialSources),
		documentedRegions: strings.Fields(bedrockCommercialSources + " " + bedrockGovCloudSources),
	},
	"anthropic.claude-haiku-4-5-20251001-v1:0": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-haiku-4-5.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-haiku-4-5-20251001-v1:0", "us-east-1 us-east-2 us-west-1 us-west-2 ca-central-1"),
			bedrockProfile("eu.anthropic.claude-haiku-4-5-20251001-v1:0", bedrockEUSources),
			bedrockProfile("au.anthropic.claude-haiku-4-5-20251001-v1:0", bedrockAUNZSources),
			bedrockProfile("jp.anthropic.claude-haiku-4-5-20251001-v1:0", bedrockJPSources),
		},
		globalProfile:     bedrockProfile("global.anthropic.claude-haiku-4-5-20251001-v1:0", bedrockCommercialSources),
		documentedRegions: strings.Fields(bedrockCommercialSources),
	},
	"anthropic.claude-opus-4-1-20250805-v1:0": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-1.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-opus-4-1-20250805-v1:0", "us-east-1 us-east-2 us-west-2"),
		},
		documentedRegions: strings.Fields("us-east-1 us-east-2 us-west-2"),
	},
	"anthropic.claude-sonnet-4-20250514-v1:0": {
		sourceURL: "https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-sonnet-4.html",
		geoProfiles: []bedrockInferenceProfile{
			bedrockProfile("us.anthropic.claude-sonnet-4-20250514-v1:0", "us-east-1 us-east-2 us-west-1 us-west-2"),
			bedrockProfile("eu.anthropic.claude-sonnet-4-20250514-v1:0", "eu-central-1 eu-north-1 eu-south-1 eu-south-2 eu-west-1 eu-west-3 il-central-1"),
			bedrockProfile("apac.anthropic.claude-sonnet-4-20250514-v1:0", "ap-northeast-1 ap-northeast-2 ap-northeast-3 ap-south-1 ap-south-2 ap-southeast-1 ap-southeast-2"),
		},
		globalProfile:        bedrockProfile("global.anthropic.claude-sonnet-4-20250514-v1:0", "us-east-1 us-east-2 us-west-2 eu-west-1 ap-northeast-1"),
		documentedRegions:    strings.Fields("us-east-1 us-east-2 us-west-1 us-west-2 eu-central-1 eu-north-1 eu-south-1 eu-south-2 eu-west-1 eu-west-3 ap-east-2 ap-northeast-1 ap-northeast-2 ap-northeast-3 ap-south-1 ap-south-2 ap-southeast-1 ap-southeast-2 ap-southeast-3 ap-southeast-4 ap-southeast-5 ap-southeast-7 il-central-1"),
		unverifiedGeoRegions: strings.Fields("ap-east-2 ap-southeast-3 ap-southeast-4 ap-southeast-5 ap-southeast-7"),
	},
	// Opus 4 的当前模型详情页已不可用，旧型号概览不能证明具体来源区域支持；保留显式的未核实状态。
	"anthropic.claude-opus-4-20250514-v1:0": {
		sourceURL: "https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock-legacy",
	},
}
