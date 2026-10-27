package config

import (
	"net/netip"
	"regexp"
	"strings"

	"github.com/swiss-subnet/setupos-forge/pkg/deterministic"
)

const (
	srcCheckHardware = "ic-os/components/setupos/check-hardware.sh"
	srcCheckNetwork  = "ic-os/components/setupos/check-network.sh"
)

var (
	// main.rs accepts one digit after the dot, so type4.10 is rejected here.
	rewardTypeRe = regexp.MustCompile(`^type[0-9]+(\.[0-9])?$`)

	// check-hardware.sh accepts several digits, and only these families. The
	// two patterns disagree, so a value can pass config_tool and still halt
	// the install.
	gen1Re = regexp.MustCompile(`^type(0|1)(\.[0-9]+)?$`)
	gen2Re = regexp.MustCompile(`^type3(\.[0-9]+)?$`)
	gen3Re = regexp.MustCompile(`^type4(\.[0-9]+)?$`)

	domainPartRe = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)
)

// Generation is the hardware generation check-hardware.sh infers from the
// reward type. It decides which hardware checks run.
type Generation int

const (
	GenUnknown Generation = iota
	Gen1
	Gen2
	Gen3
)

func (g Generation) String() string {
	switch g {
	case Gen1:
		return "gen1"
	case Gen2:
		return "gen2"
	case Gen3:
		return "gen3 (cloud engine)"
	}
	return "unknown"
}

// GenerationFor maps a reward type the way detect_hardware_generation does.
func GenerationFor(rewardType string) Generation {
	switch {
	case gen1Re.MatchString(rewardType):
		return Gen1
	case gen2Re.MatchString(rewardType):
		return Gen2
	case gen3Re.MatchString(rewardType):
		return Gen3
	}
	return GenUnknown
}

// validIPv6Prefix mirrors is_valid_ipv6_prefix, which always appends "::"
// before parsing. A prefix that already ends in "::" therefore fails.
//
// Note "" passes here, exactly as it does upstream, because "" + "::" parses.
// An `ipv6_prefix=` line with no value is accepted by SetupOS and puts the
// node in ::/64. Validate catches the empty case separately.
func validIPv6Prefix(prefix string) bool {
	if len(prefix) > 19 {
		return false
	}
	addr, err := netip.ParseAddr(prefix + "::")
	return err == nil && addr.Is6() && !addr.Is4In6() && addr.Zone() == ""
}

// Validate reports everything SetupOS would reject, plus the mistakes it
// accepts and then fails on at the rack.
func (c Config) Validate() Problems {
	var ps Problems
	ps = c.validateIPv6(ps)
	ps = c.validateIPv4(ps)
	ps = c.validateDomain(ps)
	ps = c.validateRewardType(ps)
	return ps
}

func (c Config) validateIPv6(ps Problems) Problems {
	switch {
	case c.IPv6Prefix == "":
		ps.add(SeverityError, "ipv6_prefix", srcConfigIni, "required")
	case !validIPv6Prefix(c.IPv6Prefix):
		ps.add(SeverityError, "ipv6_prefix", srcConfigIni,
			"%q is not a valid prefix: at most 19 characters, and it must not contain \"::\" or a trailing colon", c.IPv6Prefix)
	}

	if !c.IPv6Gateway.IsValid() {
		ps.add(SeverityError, "ipv6_gateway", srcConfigIni, "required")
		return ps
	}

	// A link-local gateway is legitimate and lives outside the prefix by
	// definition, so only a global gateway is checked for containment.
	if c.IPv6Gateway.IsLinkLocalUnicast() || c.IPv6Prefix == "" || !validIPv6Prefix(c.IPv6Prefix) {
		return ps
	}
	pfx, err := deterministic.ParsePrefix(c.IPv6Prefix)
	if err == nil && !pfx.Contains(c.IPv6Gateway) {
		ps.add(SeverityWarning, "ipv6_gateway", srcCheckNetwork,
			"%s is outside %s. Unless the router makes it on-link, ping_ipv6_gateway halts the install after a 60 second timeout", c.IPv6Gateway, pfx)
	}
	return ps
}

func (c Config) validateIPv4(ps Problems) Problems {
	if c.IPv4 == nil {
		if c.Domain != "" {
			ps.add(SeverityWarning, "domain", srcCheckNetwork,
				"set without an IPv4 block. It is only required alongside IPv4, and its syntax is still checked at install time")
		}
		return ps
	}

	lengthOK := c.IPv4.PrefixLength >= 0 && c.IPv4.PrefixLength <= 32
	if !lengthOK {
		ps.add(SeverityError, "ipv4_prefix_length", srcConfigIni,
			"%d is out of range, must be 0 to 32", c.IPv4.PrefixLength)
	}
	if c.Domain == "" {
		ps.add(SeverityWarning, "domain", srcCheckNetwork,
			"an IPv4 node needs a domain with matching A and AAAA records")
	}

	if !lengthOK || !c.IPv4.Address.Is4() || !c.IPv4.Gateway.Is4() {
		return ps
	}
	subnet := netip.PrefixFrom(c.IPv4.Address, c.IPv4.PrefixLength).Masked()
	if !subnet.Contains(c.IPv4.Gateway) {
		ps.add(SeverityWarning, "ipv4_gateway", srcCheckNetwork,
			"%s is outside %s. ping_ipv4_gateway halts the install after a 60 second timeout", c.IPv4.Gateway, subnet)
	}
	return ps
}

// validateDomain mirrors validate_domain_name, which runs whenever domain is
// non-empty, IPv4 or not.
func (c Config) validateDomain(ps Problems) Problems {
	if c.Domain == "" {
		return ps
	}
	parts := strings.Split(c.Domain, ".")
	if len(parts) < 2 {
		ps.add(SeverityError, "domain", srcCheckNetwork, "%q has fewer than two parts", c.Domain)
	}
	for _, part := range parts {
		switch {
		case part == "":
			ps.add(SeverityError, "domain", srcCheckNetwork, "%q has an empty part", c.Domain)
		case len(part) > 63:
			ps.add(SeverityError, "domain", srcCheckNetwork, "part %q is longer than 63 characters", part)
		case strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-"):
			ps.add(SeverityError, "domain", srcCheckNetwork, "part %q starts or ends with a hyphen", part)
		case !domainPartRe.MatchString(part):
			ps.add(SeverityError, "domain", srcCheckNetwork, "part %q has characters outside [a-zA-Z0-9-]", part)
		}
	}
	return ps
}

func (c Config) validateRewardType(ps Problems) Problems {
	if c.NodeRewardType == "" {
		// config_ini.rs treats this as optional, but check-hardware.sh then
		// has nothing to infer a generation from and halts.
		ps.add(SeverityError, "node_reward_type", srcCheckHardware,
			"required: detect_hardware_generation halts on an empty reward type")
		return ps
	}
	if !rewardTypeRe.MatchString(c.NodeRewardType) {
		ps.add(SeverityError, "node_reward_type", srcMain,
			"%q does not match ^type[0-9]+(\\.[0-9])?$; note only one digit is allowed after the dot", c.NodeRewardType)
	}

	gen := GenerationFor(c.NodeRewardType)
	if gen == GenUnknown {
		ps.add(SeverityError, "node_reward_type", srcCheckHardware,
			"%q maps to no hardware generation. Only type0, type1 (gen1), type3 (gen2) and type4 (gen3) are recognised", c.NodeRewardType)
	}

	if c.EnableTrustedExecutionEnvironment && gen != Gen2 {
		ps.add(SeverityError, "enable_trusted_execution_environment", srcCheckHardware,
			"trusted execution is gen2 only, but %q is %s. verify_sev_snp halts the install", c.NodeRewardType, gen)
	}
	return ps
}
