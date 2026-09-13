package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ccVersionInBillingRe 匹配 cc_version 中的三段版本号。
var ccVersionInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+`)

var ccVersionWithFingerprintInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+\.[0-9a-fA-F]{3}\b`)

// OAuth 伪装会在账号指纹之后强制写入运行时 CLI User-Agent，计费标记必须与其一致。
// @project-doc docs/interfaces/anthropic_upstream.md#claude_billing_fingerprint
func effectiveBillingUserAgent(tokenType string, mimicClaudeCode bool, fingerprint *Fingerprint) string {
	if tokenType == "oauth" && mimicClaudeCode {
		return claude.DefaultHeaders["User-Agent"]
	}
	if fingerprint == nil {
		return ""
	}
	return fingerprint.UserAgent
}

// syncBillingHeaderVersion 将计费标记的版本和指纹后缀同步到实际出站 User-Agent。
// 后缀依赖版本和用户消息，因此版本改变后必须重算；只处理 system 数组中的计费标记。
func syncBillingHeaderVersion(body []byte, userAgent string) []byte {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body
	}

	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	replacement := "cc_version=" + version
	idx := 0
	systemResult.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if text.Exists() && text.Type == gjson.String &&
			strings.HasPrefix(text.String(), "x-anthropic-billing-header") {
			fingerprintedReplacement := replacement + "." + computeClaudeCodeFingerprint(body, version)
			newText := ccVersionWithFingerprintInBillingRe.ReplaceAllString(text.String(), fingerprintedReplacement)
			newText = ccVersionInBillingRe.ReplaceAllString(newText, replacement)
			if newText != text.String() {
				if updated, err := sjson.SetBytes(body, fmt.Sprintf("system.%d.text", idx), newText); err == nil {
					body = updated
				}
			}
		}
		idx++
		return true
	})

	return body
}
