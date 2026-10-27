// Package deterministic derives the MAC and IPv6 addresses that IC-OS assigns
// to a node, from that node's BMC MAC address.
//
// IC-OS does not use the data NIC's factory MAC. It hashes the BMC MAC to a
// synthetic MAC, writes that onto the data interface, and takes the address as
// EUI-64 over the result. So every address a node will ever hold is knowable
// before the machine is racked.
//
// Ported from dfinity/ic at b9f8bbf07a1aca5212ff30c7690c08d6ceeb9566:
//
//	rs/ic_os/networking/deterministic_ips/src/lib.rs
//	rs/ic_os/networking/deterministic_ips/src/node_type.rs
package deterministic

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Environment is the deployment environment, and is part of the hash seed. A
// node derives different addresses on mainnet than on testnet.
type Environment string

const (
	Mainnet Environment = "mainnet"
	Testnet Environment = "testnet"
)

// Valid reports whether e is an environment IC-OS accepts.
func (e Environment) Valid() bool { return e == Mainnet || e == Testnet }

// NodeType selects which of a machine's several identities is being derived.
// The value is the index byte that lands in the second octet of the MAC.
type NodeType byte

const (
	HostOS         NodeType = 0x00
	GuestOS        NodeType = 0x01
	UpgradeGuestOS NodeType = 0x02
	SetupOS        NodeType = 0x0f
)

func (n NodeType) String() string {
	switch n {
	case HostOS:
		return "HostOS"
	case GuestOS:
		return "GuestOS"
	case UpgradeGuestOS:
		return "UpgradeGuestOS"
	case SetupOS:
		return "SetupOS"
	}
	return fmt.Sprintf("NodeType(%#02x)", byte(n))
}

// Slot identifies a GuestOS VM on a host running more than one. SlotPlain is
// the single-guest case and is what SetupOS installs today.
type Slot byte

const SlotPlain Slot = 0

// Interface is one derived identity: the MAC IC-OS will write onto the data
// NIC, and the address it takes from it.
type Interface struct {
	MAC  net.HardwareAddr
	IPv6 netip.Addr
}

// Node is the set of identities a single machine holds over its lifecycle.
type Node struct {
	SetupOS Interface // the installer, only while the USB is booted
	HostOS  Interface // the hypervisor, for the life of the machine
	GuestOS Interface // the replica VM
}

// ParsePrefix interprets a config.ini ipv6_prefix as the /64 it names.
//
// The prefix is the leading hextets with the trailing "::" optional, so both
// "2001:db8:1:1" and "2001:db8:1:1::" name the same /64. Upstream hardcodes
// the length to 64 and never reads one from the file.
func ParsePrefix(prefix string) (netip.Prefix, error) {
	full := prefix
	if !strings.Contains(full, "::") {
		full += "::"
	}
	addr, err := netip.ParseAddr(full)
	if err != nil || !addr.Is6() || addr.Is4In6() || addr.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("deterministic: invalid IPv6 prefix %q", prefix)
	}
	octets := addr.As16()
	var base [16]byte
	copy(base[:8], octets[:8])
	return netip.PrefixFrom(netip.AddrFrom16(base), 64), nil
}

// MAC derives the synthetic MAC for one identity on the machine whose BMC
// holds bmc.
func MAC(bmc net.HardwareAddr, env Environment, nt NodeType, slot Slot) (net.HardwareAddr, error) {
	if len(bmc) != 6 {
		return nil, fmt.Errorf("deterministic: BMC MAC must be 6 bytes, got %d", len(bmc))
	}
	if !env.Valid() {
		return nil, fmt.Errorf("deterministic: unknown environment %q", env)
	}

	// The trailing newline is load-bearing. Upstream keeps it to stay
	// compatible with the shell scripts this logic replaced.
	sum := sha256.Sum256([]byte(strings.ToLower(bmc.String()) + string(env) + "\n"))

	if slot == SlotPlain {
		// Locally administered, unicast.
		return net.HardwareAddr{0x6a, byte(nt), sum[0], sum[1], sum[2], sum[3]}, nil
	}
	// Multi-guest hosts take a separate prefix so the slot can occupy the
	// byte sum[0] would have, costing 8 bits against cross-machine collisions.
	return net.HardwareAddr{0x7a, byte(nt), byte(slot), sum[1], sum[2], sum[3]}, nil
}

// SLAAC combines a /64 prefix with the EUI-64 of mac.
func SLAAC(mac net.HardwareAddr, prefix string) (netip.Addr, error) {
	if len(mac) != 6 {
		return netip.Addr{}, fmt.Errorf("deterministic: MAC must be 6 bytes, got %d", len(mac))
	}
	pfx, err := ParsePrefix(prefix)
	if err != nil {
		return netip.Addr{}, err
	}

	out := pfx.Addr().As16()
	out[8] = mac[0] ^ 0x02 // flip the universal/local bit
	out[9], out[10] = mac[1], mac[2]
	out[11], out[12] = 0xff, 0xfe
	out[13], out[14], out[15] = mac[3], mac[4], mac[5]
	return netip.AddrFrom16(out), nil
}

// Address derives one identity end to end.
func Address(bmc net.HardwareAddr, env Environment, nt NodeType, slot Slot, prefix string) (Interface, error) {
	mac, err := MAC(bmc, env, nt, slot)
	if err != nil {
		return Interface{}, err
	}
	addr, err := SLAAC(mac, prefix)
	if err != nil {
		return Interface{}, err
	}
	return Interface{MAC: mac, IPv6: addr}, nil
}

// Derive returns the three identities a single-guest machine takes.
func Derive(bmc net.HardwareAddr, env Environment, prefix string) (Node, error) {
	var n Node
	for _, step := range []struct {
		nt  NodeType
		dst *Interface
	}{
		{SetupOS, &n.SetupOS},
		{HostOS, &n.HostOS},
		{GuestOS, &n.GuestOS},
	} {
		iface, err := Address(bmc, env, step.nt, SlotPlain, prefix)
		if err != nil {
			return Node{}, err
		}
		*step.dst = iface
	}
	return n, nil
}
