package safety

import (
	"net/netip"
)

type AddressError struct {
	Addr   netip.Addr
	Reason string
}

func (e *AddressError) Error() string {
	return "address " + e.Addr.String() + " blocked: " + e.Reason
}

type blockedRange struct {
	prefix netip.Prefix
	reason string
}

var blockedRanges = []blockedRange{
	{netip.MustParsePrefix("0.0.0.0/8"), "this network"},
	{netip.MustParsePrefix("10.0.0.0/8"), "private network"},
	{netip.MustParsePrefix("100.64.0.0/10"), "carrier-grade nat"},
	{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local, includes cloud metadata"},
	{netip.MustParsePrefix("172.16.0.0/12"), "private network"},
	{netip.MustParsePrefix("192.0.0.0/24"), "ietf protocol assignments"},
	{netip.MustParsePrefix("192.0.2.0/24"), "documentation"},
	{netip.MustParsePrefix("192.168.0.0/16"), "private network"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking"},
	{netip.MustParsePrefix("198.51.100.0/24"), "documentation"},
	{netip.MustParsePrefix("203.0.113.0/24"), "documentation"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved"},

	{netip.MustParsePrefix("::/128"), "unspecified"},
	{netip.MustParsePrefix("::1/128"), "loopback"},
	{netip.MustParsePrefix("64:ff9b::/96"), "nat64"},
	{netip.MustParsePrefix("64:ff9b:1::/48"), "local-use nat64"},
	{netip.MustParsePrefix("100::/64"), "discard-only"},
	{netip.MustParsePrefix("2001::/32"), "teredo"},
	{netip.MustParsePrefix("2001:db8::/32"), "documentation"},
	{netip.MustParsePrefix("2002::/16"), "6to4"},
	{netip.MustParsePrefix("fc00::/7"), "unique local"},
	{netip.MustParsePrefix("fe80::/10"), "link-local"},
	{netip.MustParsePrefix("ff00::/8"), "multicast"},
}

func CheckAddr(addr netip.Addr) error {
	if !addr.IsValid() {
		return &AddressError{addr, "invalid"}
	}
	if addr.Is4In6() {
		return &AddressError{addr, "ipv4-mapped ipv6"}
	}
	if addr.Zone() != "" {
		return &AddressError{addr, "zoned address"}
	}
	for _, r := range blockedRanges {
		if r.prefix.Contains(addr) {
			return &AddressError{addr, r.reason}
		}
	}
	return nil
}
