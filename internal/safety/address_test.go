package safety

import (
	"net/netip"
	"testing"
)

func TestCheckAddrBlocks(t *testing.T) {
	blocked := []string{
		"0.0.0.0",
		"0.1.2.3",
		"10.0.0.1",
		"10.255.255.255",
		"100.64.0.1",
		"127.0.0.1",
		"127.1.2.3",
		"169.254.169.254",
		"169.254.0.1",
		"172.16.0.1",
		"172.31.255.255",
		"192.0.0.1",
		"192.0.2.1",
		"192.168.1.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"224.0.0.1",
		"239.255.255.250",
		"240.0.0.1",
		"255.255.255.255",

		"::",
		"::1",
		"::ffff:127.0.0.1",
		"::ffff:8.8.8.8",
		"64:ff9b::7f00:1",
		"64:ff9b:1::1",
		"100::1",
		"2001::1",
		"2001:db8::1",
		"2002:7f00:1::1",
		"fc00::1",
		"fd00::1",
		"fe80::1",
		"ff02::1",
	}

	for _, s := range blocked {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if err := CheckAddr(addr); err == nil {
			t.Errorf("%s was allowed, want blocked", s)
		}
	}
}

func TestCheckAddrAllows(t *testing.T) {
	allowed := []string{
		"1.1.1.1",
		"8.8.8.8",
		"93.184.216.34",
		"172.15.255.255",
		"172.32.0.1",
		"192.167.255.255",
		"192.169.0.1",
		"100.63.255.255",
		"100.128.0.1",
		"2606:4700:4700::1111",
		"2a00:1450:4001:80f::200e",
	}

	for _, s := range allowed {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if err := CheckAddr(addr); err != nil {
			t.Errorf("%s was blocked (%v), want allowed", s, err)
		}
	}
}

func TestCheckAddrInvalid(t *testing.T) {
	if err := CheckAddr(netip.Addr{}); err == nil {
		t.Error("zero address was allowed")
	}
	zoned := netip.MustParseAddr("2606:4700:4700::1111").WithZone("eth0")
	if err := CheckAddr(zoned); err == nil {
		t.Error("zoned address was allowed")
	}
}

func FuzzCheckAddr(f *testing.F) {
	seeds := []string{
		"127.0.0.1", "8.8.8.8", "::1", "::ffff:169.254.169.254",
		"fe80::1%eth0", "2002::1", "", "not an address",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return
		}
		if CheckAddr(addr) != nil {
			return
		}
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
			addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
			addr.IsMulticast() || addr.IsInterfaceLocalMulticast() || addr.Is4In6() {
			t.Fatalf("%s passed CheckAddr but stdlib classifies it as non-public", addr)
		}
	})
}
