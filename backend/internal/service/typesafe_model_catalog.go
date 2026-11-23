package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// 用独立错误区分未知公开型号和暂时无法读取账号配置。
var (
	ErrTypeSafeModelNotDeclared        = errors.New("TypeSafe model is not configured for this group")
	ErrTypeSafeModelCatalogUnavailable = errors.New("TypeSafe model catalog is unavailable")
)

type typeSafeModelDeclarationKey struct{}

// 声明规则确定公开请求型号 R 能否由单个账号声明，未声明的名字不能借助通配改写变成可调用的私有目标。
type typeSafeModelDeclaration struct {
	name                string
	publicChannelSource bool
	channelIntermediate bool
}

func typeSafeDeclaration(channel *Channel, name string) typeSafeModelDeclaration {
	rule := typeSafeModelDeclaration{name: strings.TrimSpace(name)}
	if rule.name == "" || strings.Contains(rule.name, "*") {
		return rule
	}
	rule.publicChannelSource = rule.name == DefaultTypeSafeModel
	if channel == nil || rule.publicChannelSource {
		return rule
	}
	mapping := channel.ModelMapping[PlatformTypeSafe]
	if _, exact := mapping[rule.name]; exact {
		rule.publicChannelSource = true
		return rule
	}
	for _, target := range mapping {
		if target == rule.name {
			rule.channelIntermediate = true
			break
		}
	}
	return rule
}

// 账号精确映射源与最终白名单分别声明公开名。只作为渠道中间项的映射源不单独发布。
func (r typeSafeModelDeclaration) accepts(account *Account) bool {
	if r.publicChannelSource {
		return true
	}
	if r.name == "" || strings.Contains(r.name, "*") {
		return false
	}
	mapping := account.GetModelMapping()
	whitelist, _ := resolveFinalModelWhitelist(account.Platform, account.Credentials, mapping)
	if _, allowed := whitelist[r.name]; allowed {
		return true
	}
	target, exact := mapping[r.name]
	return exact && (!r.channelIntermediate || target == r.name)
}

// 只从当前账号配置和渠道精确来源构造有限目录；通配、价格记录和缓存均不能产生新身份。
func typeSafePublicModelCandidates(accounts []Account, channel *Channel) []string {
	names := map[string]bool{DefaultTypeSafeModel: true}
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model != "" && !strings.Contains(model, "*") {
			names[model] = true
		}
	}
	intermediate := make(map[string]bool)
	if channel != nil {
		for src, target := range channel.ModelMapping[PlatformTypeSafe] {
			add(src)
			intermediate[target] = true
		}
	}
	for i := range accounts {
		account := &accounts[i]
		if !account.IsTypeSafe() {
			continue
		}
		mapping := account.GetModelMapping()
		whitelist, _ := resolveFinalModelWhitelist(account.Platform, account.Credentials, mapping)
		for model := range whitelist {
			add(model)
		}
		for source, target := range mapping {
			if !intermediate[source] || source == target {
				add(source)
			}
		}
	}
	models := make([]string, 0, len(names))
	for model := range names {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// 调度无可用账号时，通过持久配置区分未声明型号与临时不可用。
func (s *OpenAIGatewayService) diagnoseTypeSafeDeclaration(ctx context.Context, groupID *int64, declaration *typeSafeModelDeclaration) error {
	if s.accountRepo == nil {
		return ErrTypeSafeModelCatalogUnavailable
	}
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, groupID, []string{PlatformTypeSafe}, false)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTypeSafeModelCatalogUnavailable, err)
	}
	for i := range accounts {
		if declaration.accepts(&accounts[i]) {
			return nil
		}
	}
	return ErrTypeSafeModelNotDeclared
}
