package repository

import (
	"context"
	"syscall"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/TokenFlux/TokenRouter/internal/util/urlvalidator"
)

// @project-doc docs/operations/edge_security.md#outbound_ssrf_dial_guard
// upstreamDialControlContext 返回上游直连拨号使用的 ControlContext 钩子。
//
// 请求前的 ValidateResolvedIP 只能检查“那一次”解析结果，真正拨号时会重新解析域名，
// 攻击者控制的域名可借此在校验后改指向内网（DNS Rebinding）。这里在套接字 connect 前
// 校验拨号器最终选定的 IP，校验与连接必然是同一地址。
//
// 两类请求需要校验：
//   - alwaysPublicOnly：全局开启 URL 白名单且禁止私网（与 shouldValidateResolvedIP 相同口径）；
//   - 单次请求带 WithHTTPUpstreamPublicHostsOnly 标记（如图片 URL 回填下载），
//     拨号上下文即请求上下文，因此可逐请求判定，不影响共享连接池里的其它请求。
//
// 平台原有的 Linux TCP_USER_TIMEOUT 钩子保持不变，在公网校验通过后继续执行。
func upstreamDialControlContext(alwaysPublicOnly bool) func(ctx context.Context, network, address string, c syscall.RawConn) error {
	// 平台套接字钩子在 Linux 为函数、其它平台为 nil 变量；统一赋给函数类型变量后再判空。
	var platformControl func(network, address string, c syscall.RawConn) error = upstreamDialControl
	return func(ctx context.Context, network, address string, c syscall.RawConn) error {
		if alwaysPublicOnly || service.HTTPUpstreamPublicHostsOnly(ctx) {
			if err := urlvalidator.PublicOnlyDialControl(network, address, c); err != nil {
				return err
			}
		}
		if platformControl != nil {
			return platformControl(network, address, c)
		}
		return nil
	}
}
