package urlvalidator

import (
	"fmt"
	"net"
	"syscall"
)

// IsDisallowedIP 报告 ip 是否为不允许外发连接的地址（回环、私网、链路本地、未指定）。
// 与 ValidateResolvedIP 使用同一判定，保证预检与拨号时的口径一致。
func IsDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// @project-doc docs/operations/edge_security.md#outbound_ssrf_dial_guard
// PublicOnlyDialControl 是 net.Dialer.Control 钩子：在套接字 connect 之前检查本次实际要连接的 IP。
//
// 仅在请求前解析并校验域名无法防御 DNS Rebinding：校验后的第二次解析（真正拨号时）
// 可能返回内网地址。Control 钩子拿到的是拨号器最终选定的 IP，因此校验与连接必然是同一地址；
// 域名每次重新解析得到的每个候选 IP 都会逐一经过此检查。TLS 仍使用原始主机名做 SNI 与证书校验。
//
// 走代理时连接的是代理地址而非目标，调用方不应对代理拨号启用此钩子。
func PublicOnlyDialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("dial guard: invalid address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("dial guard: address %q is not an ip", address)
	}
	if IsDisallowedIP(ip) {
		return fmt.Errorf("dial guard: connecting to %s is not allowed", ip.String())
	}
	return nil
}

// ChainDialControl 依次执行多个 Control 钩子，任一返回错误即中止拨号；nil 钩子会被跳过。
func ChainDialControl(controls ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	active := make([]func(network, address string, c syscall.RawConn) error, 0, len(controls))
	for _, control := range controls {
		if control != nil {
			active = append(active, control)
		}
	}
	if len(active) == 0 {
		return nil
	}
	if len(active) == 1 {
		return active[0]
	}
	return func(network, address string, c syscall.RawConn) error {
		for _, control := range active {
			if err := control(network, address, c); err != nil {
				return err
			}
		}
		return nil
	}
}
