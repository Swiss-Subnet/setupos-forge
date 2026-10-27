package deterministic

import (
	"net"
	"net/netip"
	"testing"
)

// Every case below is lifted from the upstream test module at
// rs/ic_os/networking/deterministic_ips/src/lib.rs. If one of these starts
// failing after an upstream bump, the derivation changed and every address
// this tool has ever printed is suspect.

func mustMAC(t *testing.T, s string) net.HardwareAddr {
	t.Helper()
	m, err := net.ParseMAC(s)
	if err != nil {
		t.Fatalf("ParseMAC(%q): %v", s, err)
	}
	return m
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}

func TestMAC(t *testing.T) {
	tests := []struct {
		name string
		bmc  string
		env  Environment
		nt   NodeType
		slot Slot
		want string
	}{
		// upstream: mac
		{"hostos testnet", "70:B5:E8:E8:25:DE", Testnet, HostOS, SlotPlain, "6a:00:f8:87:a4:8a"},
		// upstream: multi_mac
		{"upgrade guestos slot 240", "70:B5:E8:E8:25:DE", Testnet, UpgradeGuestOS, Slot(240), "7a:02:f0:87:a4:8a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MAC(mustMAC(t, tt.bmc), tt.env, tt.nt, tt.slot)
			if err != nil {
				t.Fatalf("MAC: %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("MAC = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMACRejects(t *testing.T) {
	if _, err := MAC(net.HardwareAddr{1, 2, 3}, Mainnet, HostOS, SlotPlain); err == nil {
		t.Error("short MAC accepted")
	}
	// A typo'd environment must not silently produce a different address.
	if _, err := MAC(mustMAC(t, "70:B5:E8:E8:25:DE"), Environment("Mainnet"), HostOS, SlotPlain); err == nil {
		t.Error("bad environment accepted")
	}
}

func TestSLAAC(t *testing.T) {
	tests := []struct {
		name   string
		mac    string
		prefix string
		want   string
	}{
		// upstream: test_calculate_slaac
		{"plain", "6a:01:e5:96:2d:49", "2a04:9dc0:0:108", "2a04:9dc0:0:108:6801:e5ff:fe96:2d49"},
		// upstream: test_prefix_without_suffix
		{"short prefix", "6a:01:e5:96:2d:49", "2001:db8", "2001:db8::6801:e5ff:fe96:2d49"},
		// upstream: test_prefix_with_double_colon
		{"explicit ::", "6a:01:e5:96:2d:49", "2a04:9dc0:0:108::", "2a04:9dc0:0:108:6801:e5ff:fe96:2d49"},
		// upstream: test_all_zero_mac
		{"zero mac", "00:00:00:00:00:00", "2001:db8::", "2001:db8::200:ff:fe00:0"},
		// upstream: test_all_one_mac
		{"ones mac", "ff:ff:ff:ff:ff:ff", "2001:db8::", "2001:db8::fdff:ffff:feff:ffff"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SLAAC(mustMAC(t, tt.mac), tt.prefix)
			if err != nil {
				t.Fatalf("SLAAC: %v", err)
			}
			if got != mustAddr(t, tt.want) {
				t.Errorf("SLAAC = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSLAACInvalidPrefix(t *testing.T) {
	// upstream: test_invalid_prefix
	for _, prefix := range []string{"invalid_prefix", "gggg::", "2a04:9dc0:0:108:zzzz", "1234:::"} {
		if _, err := SLAAC(mustMAC(t, "6a:01:e5:96:2d:49"), prefix); err == nil {
			t.Errorf("prefix %q accepted, want rejected", prefix)
		}
	}
	// Ours, not upstream's: a zone would silently change the meaning.
	if _, err := ParsePrefix("2001:db8::%eth0"); err == nil {
		t.Error("zoned prefix accepted")
	}
}

func TestAddress(t *testing.T) {
	tests := []struct {
		name   string
		bmc    string
		env    Environment
		nt     NodeType
		slot   Slot
		prefix string
		want   string
	}{
		// upstream: mac_to_slaac
		{"guestos", "b0:7b:25:c8:f6:c0", Mainnet, GuestOS, SlotPlain, "2602:FFE4:801:17", "2602:ffe4:801:17:6801:ff:feec:bd51"},
		// upstream: multi_mac_to_slaac
		{"upgrade guestos slot 241", "b0:7b:25:c8:f6:c0", Mainnet, UpgradeGuestOS, Slot(241), "2602:FFE4:801:17", "2602:ffe4:801:17:7802:f1ff:feec:bd51"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Address(mustMAC(t, tt.bmc), tt.env, tt.nt, tt.slot, tt.prefix)
			if err != nil {
				t.Fatalf("Address: %v", err)
			}
			if got.IPv6 != mustAddr(t, tt.want) {
				t.Errorf("Address = %s, want %s", got.IPv6, tt.want)
			}
		})
	}
}

// upstream: multi_slaac_similarities
func TestDeriveSharesPrefixAndSuffix(t *testing.T) {
	const prefix = "2602:FFE4:801:17"
	bmc := mustMAC(t, "b0:7b:25:c8:f6:c0")

	n, err := Derive(bmc, Mainnet, prefix)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	host := n.HostOS.IPv6.String()
	ipPrefix := host[:17]
	ipSuffix := host[len(host)-12:]

	multi, err := Address(bmc, Mainnet, GuestOS, Slot(1), prefix)
	if err != nil {
		t.Fatalf("Address: %v", err)
	}

	for _, got := range []string{n.GuestOS.IPv6.String(), multi.IPv6.String()} {
		if got[:17] != ipPrefix {
			t.Errorf("%s does not share prefix %s", got, ipPrefix)
		}
		if got[len(got)-12:] != ipSuffix {
			t.Errorf("%s does not share suffix %s", got, ipSuffix)
		}
	}
}

// The three identities of one machine must never collide with each other.
func TestDeriveDistinct(t *testing.T) {
	// RFC 7042 documentation MAC block, so no real machine is named here.
	n, err := Derive(mustMAC(t, "00:00:5e:00:53:01"), Mainnet, "2001:db8::")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	seen := map[netip.Addr]string{}
	for name, iface := range map[string]Interface{"setupos": n.SetupOS, "hostos": n.HostOS, "guestos": n.GuestOS} {
		if other, dup := seen[iface.IPv6]; dup {
			t.Errorf("%s and %s both derive %s", name, other, iface.IPv6)
		}
		seen[iface.IPv6] = name
	}
}
