package service

import (
	"context"
	"strings"
)

// gmailFamilyDomains 列出 Gmail 及其历史别名域名。两者投递到同一收件箱。
var gmailFamilyDomains = map[string]struct{}{
	"gmail.com":      {},
	"googlemail.com": {},
}

// NormalizeEmailForAliasDedup 将邮箱折叠为收件箱身份，只用于别名查重，
// 不改变数据库中保存的地址，也不改变登录和发信地址。
// 所有域名去掉本地部分的 + 后缀；Gmail 家族另外忽略本地部分点号并统一域名。
func NormalizeEmailForAliasDedup(email string) string {
	local, domain, ok := splitEmailForAliasDedup(email)
	if !ok {
		return strings.ToLower(strings.TrimSpace(email))
	}
	local = stripEmailPlusSuffix(local)
	if isGmailFamilyDomain(domain) {
		local = stripEmailLocalDots(local)
		domain = "gmail.com"
	}
	return local + "@" + domain
}

// EmailAliasProbe 描述 SQL 别名探针的本地部分和域名部分。
// 探针会去除点号以覆盖 Gmail 点号及域名根点；查询命中后仍须重新执行完整归一化。
type EmailAliasProbe struct {
	Local  string
	Domain string
}

// EmailAliasDedupProbes 返回可能与 email 指向同一收件箱的 SQL 探针。
// 输入格式无效或本地部分去点后为空时返回 nil。
func EmailAliasDedupProbes(email string) []EmailAliasProbe {
	local, domain, ok := splitEmailForAliasDedup(email)
	if !ok {
		return nil
	}
	probeLocal := strings.ReplaceAll(stripEmailPlusSuffix(local), ".", "")
	if probeLocal == "" {
		return nil
	}
	domains := []string{domain}
	if isGmailFamilyDomain(domain) {
		domains = []string{"gmail.com", "googlemail.com"}
	}
	probes := make([]EmailAliasProbe, 0, len(domains))
	for _, candidate := range domains {
		probes = append(probes, EmailAliasProbe{
			Local:  probeLocal,
			Domain: strings.ReplaceAll(candidate, ".", ""),
		})
	}
	return probes
}

func splitEmailForAliasDedup(email string) (local string, domain string, ok bool) {
	local, domain, ok = splitEmailForPolicy(email)
	if !ok {
		return "", "", false
	}
	domain = strings.TrimRight(domain, ".")
	if domain == "" {
		return "", "", false
	}
	return local, domain, true
}

func stripEmailPlusSuffix(local string) string {
	// 本地部分以 + 开头时不能折叠为空，否则会把 +alice 与 +bob 误判为同一地址。
	if idx := strings.IndexByte(local, '+'); idx > 0 {
		return local[:idx]
	}
	return local
}

func stripEmailLocalDots(local string) string {
	if stripped := strings.ReplaceAll(local, ".", ""); stripped != "" {
		return stripped
	}
	return local
}

func isGmailFamilyDomain(domain string) bool {
	_, ok := gmailFamilyDomains[domain]
	return ok
}

// emailAliasLookupRepository 是邮箱别名查重的可选仓储能力。
// 保持 UserRepository 主接口不变，以兼容仅用于其他服务的测试桩。
type emailAliasLookupRepository interface {
	ExistsByEmailAlias(ctx context.Context, email string) (bool, error)
}

// emailAliasOwnerLookupRepository 能在查重时区分当前用户和其他用户。
// 该能力由真实数据库仓储提供，避免用户把自己的 alias 变体误判为冲突。
type emailAliasOwnerLookupRepository interface {
	EmailAliasOwnerID(ctx context.Context, email string, currentUserID int64) (int64, bool, error)
}
