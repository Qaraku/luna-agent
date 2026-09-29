// Package netpolicy 定义共享的公共网络边界；不能将工具流量导向回环、私网或转换地址。
package netpolicy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("64:ff9b:1::/48"),
}

func BlockedReason(ip net.IP) string {
	if ip == nil {
		return "an invalid address"
	}
	switch {
	case ip.IsUnspecified():
		return "the unspecified address"
	case ip.IsLoopback():
		return "a loopback address"
	case ip.IsPrivate():
		return "a private address"
	case ip.IsLinkLocalUnicast():
		return "a link-local address"
	case ip.IsMulticast() || ip.IsLinkLocalMulticast():
		return "a multicast address"
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return "an invalid address"
	}
	addr = addr.Unmap()
	if netip.MustParsePrefix("2002::/16").Contains(addr) {
		return "a 6to4 address, which carries another address inside it"
	}
	if netip.MustParsePrefix("64:ff9b::/96").Contains(addr) {
		return "a NAT64 address, which carries another address inside it"
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return "a reserved or translated address"
		}
	}
	if !ip.IsGlobalUnicast() {
		return "a non-unicast address"
	}
	return ""
}

// DialPublic 只解析一次并直拨核准 IP，防止 DNS 重绑定；本机接口即便有公网地址也拒绝。
func DialPublic(ctx context.Context, target string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || strings.ContainsAny(host, "%/@\r\n") || len(host) > 253 {
		return nil, fmt.Errorf("invalid public network target")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("invalid network port")
	}
	var addresses []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		addresses = []net.IPAddr{{IP: ip}}
	} else {
		addresses, err = net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("network target cannot be resolved")
		}
	}
	local, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("cannot check local network boundary")
	}
	for _, address := range addresses {
		if reason := BlockedReason(address.IP); reason != "" {
			return nil, fmt.Errorf("network target is %s; only public addresses are allowed", reason)
		}
		for _, own := range local {
			ip, _, err := net.ParseCIDR(own.String())
			if err == nil && ip.Equal(address.IP) {
				return nil, fmt.Errorf("network target is a local interface")
			}
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	for _, address := range addresses {
		conn, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("public network connection failed: %w", err)
}
