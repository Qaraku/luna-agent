package netpolicy

import (
	"net"
	"testing"
)

func TestPublicAddressRejectsPrivateAndEmbeddedTargets(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "0.0.0.0", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "255.255.255.255", "fc00::1", "fe80::1", "ff02::1", "64:ff9b::7f00:1", "64:ff9b:1::a00:1", "2002:7f00:1::1", "2001:db8::1"} {
		if BlockedReason(net.ParseIP(ip)) == "" {
			t.Errorf("allowed %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if reason := BlockedReason(net.ParseIP(ip)); reason != "" {
			t.Errorf("rejected %s: %s", ip, reason)
		}
	}
}
