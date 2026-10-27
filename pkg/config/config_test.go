package config

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
)

func ipv6Only() Config {
	return Config{
		IPv6Prefix:     "2001:db8:1:1",
		IPv6Gateway:    netip.MustParseAddr("2001:db8:1:1::1"),
		NodeRewardType: "type4.2",
	}
}

func keys(ps Problems) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Severity.String()+":"+p.Key)
	}
	return out
}

func has(ps Problems, severity Severity, key string) bool {
	for _, p := range ps {
		if p.Severity == severity && p.Key == key {
			return true
		}
	}
	return false
}

// upstream: is_valid_ipv6_prefix in config_ini.rs
func TestValidIPv6Prefix(t *testing.T) {
	for _, p := range []string{"2a00:1111:1111:1111", "2a00:111:11:11", "2602:fb2b:100:10"} {
		if !validIPv6Prefix(p) {
			t.Errorf("%q rejected, want accepted", p)
		}
	}
	for _, p := range []string{
		"2a00:1111:1111:1111:",          // trailing colon
		"2a00:1111:1111:1111:1111:1111", // over 19 characters
		"abcd::1234:5678",               // already contains ::
		"2001:db8:1:1::",                // ditto, the form people reach for first
	} {
		if validIPv6Prefix(p) {
			t.Errorf("%q accepted, want rejected", p)
		}
	}
}

func TestGenerationFor(t *testing.T) {
	tests := map[string]Generation{
		"type0":    Gen1,
		"type1.1":  Gen1,
		"type3":    Gen2,
		"type3.1":  Gen2,
		"type4":    Gen3,
		"type4.2":  Gen3,
		"type4.10": Gen3, // check-hardware.sh allows multi-digit minors
		"type2":    GenUnknown,
		"":         GenUnknown,
	}
	for in, want := range tests {
		if got := GenerationFor(in); got != want {
			t.Errorf("GenerationFor(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidateClean(t *testing.T) {
	if ps := ipv6Only().Validate(); len(ps) != 0 {
		t.Errorf("clean config produced %v", keys(ps))
	}
}

func TestValidateRewardType(t *testing.T) {
	tests := []struct {
		name     string
		reward   string
		severity Severity
	}{
		{"missing", "", SeverityError},
		// Passes main.rs but maps to no generation, so check-hardware.sh halts.
		{"unknown family", "type2.1", SeverityError},
		// Matches check-hardware.sh but not main.rs: two digits after the dot.
		{"two digit minor", "type4.10", SeverityError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ipv6Only()
			c.NodeRewardType = tt.reward
			if ps := c.Validate(); !has(ps, tt.severity, "node_reward_type") {
				t.Errorf("got %v, want a %s on node_reward_type", keys(ps), tt.severity)
			}
		})
	}
}

// verify_sev_snp halts unless the generation is 2.
func TestValidateTrustedExecution(t *testing.T) {
	c := ipv6Only()
	c.EnableTrustedExecutionEnvironment = true
	if ps := c.Validate(); !has(ps, SeverityError, "enable_trusted_execution_environment") {
		t.Errorf("type4.2 with TEE got %v, want an error", keys(ps))
	}

	c.NodeRewardType = "type3.1"
	if ps := c.Validate(); has(ps, SeverityError, "enable_trusted_execution_environment") {
		t.Errorf("type3.1 with TEE got %v, want no error", keys(ps))
	}
}

func TestValidateGatewayOutsidePrefix(t *testing.T) {
	c := ipv6Only()
	c.IPv6Gateway = netip.MustParseAddr("2001:db8:1:2::1")
	if ps := c.Validate(); !has(ps, SeverityWarning, "ipv6_gateway") {
		t.Errorf("got %v, want a warning on ipv6_gateway", keys(ps))
	}

	// A link-local gateway is outside the prefix by definition and is fine.
	c.IPv6Gateway = netip.MustParseAddr("fe80::1")
	if ps := c.Validate(); has(ps, SeverityWarning, "ipv6_gateway") {
		t.Errorf("link-local gateway got %v, want no warning", keys(ps))
	}
}

// An empty prefix passes upstream's is_valid_ipv6_prefix, so it must be
// caught as required, and must not drag the gateway check into ::/64.
func TestValidateEmptyPrefix(t *testing.T) {
	c := ipv6Only()
	c.IPv6Prefix = ""
	ps := c.Validate()
	if !has(ps, SeverityError, "ipv6_prefix") {
		t.Errorf("got %v, want an error on ipv6_prefix", keys(ps))
	}
	if has(ps, SeverityWarning, "ipv6_gateway") {
		t.Errorf("got %v, want no gateway warning when the prefix is missing", keys(ps))
	}
}

func TestValidateDomain(t *testing.T) {
	bad := []string{"example", "exa_mple.com", "-example.com", "example-.com", "a..com"}
	for _, d := range bad {
		c := ipv6Only()
		c.Domain = d
		if ps := c.Validate(); !has(ps, SeverityError, "domain") {
			t.Errorf("domain %q got %v, want an error", d, keys(ps))
		}
	}

	c := ipv6Only()
	c.Domain = "node2.example.com"
	for _, p := range c.Validate() {
		if p.Key == "domain" && p.Severity == SeverityError {
			t.Errorf("valid domain rejected: %s", p)
		}
	}
}

func TestValidateIPv4PrefixLength(t *testing.T) {
	c := ipv6Only()
	c.Domain = "node2.example.com"
	c.IPv4 = &IPv4{
		Address:      netip.MustParseAddr("212.71.124.178"),
		Gateway:      netip.MustParseAddr("212.71.124.177"),
		PrefixLength: 33,
	}
	if ps := c.Validate(); !has(ps, SeverityError, "ipv4_prefix_length") {
		t.Errorf("got %v, want an error on ipv4_prefix_length", keys(ps))
	}
}

func TestParseUpstreamExample(t *testing.T) {
	// upstream: test_get_config_ini_settings
	const in = "\n\t\r\n# COMMENT          \n\n\n\n\n" +
		"ipv6_prefix=2a00:fb01:400:200\n" +
		"ipv6_gateway=2a00:fb01:400:200::1\n" +
		"ipv4_address=212.71.124.178\n" +
		"ipv4_gateway=212.71.124.177\n" +
		"ipv4_prefix_length=28\n" +
		"domain=example.com\n" +
		"verbose=false\n"

	cfg, ps, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ps.HasErrors() {
		t.Fatalf("unexpected problems: %v", keys(ps))
	}

	if cfg.IPv6Prefix != "2a00:fb01:400:200" {
		t.Errorf("IPv6Prefix = %q", cfg.IPv6Prefix)
	}
	if cfg.IPv6Gateway != netip.MustParseAddr("2a00:fb01:400:200::1") {
		t.Errorf("IPv6Gateway = %s", cfg.IPv6Gateway)
	}
	if cfg.IPv4 == nil || cfg.IPv4.PrefixLength != 28 {
		t.Fatalf("IPv4 = %+v", cfg.IPv4)
	}
	if cfg.Domain != "example.com" || cfg.Verbose {
		t.Errorf("Domain = %q, Verbose = %v", cfg.Domain, cfg.Verbose)
	}
}

func TestParseCRLF(t *testing.T) {
	// upstream: test_config_map_from_path_crlf
	cfg, _, err := Parse(strings.NewReader("ipv6_prefix=2001:db8\r\nipv6_gateway=2001:db8::1\r\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.IPv6Prefix != "2001:db8" {
		t.Errorf("IPv6Prefix = %q, want the value without the CR", cfg.IPv6Prefix)
	}
}

func TestParseInlineComment(t *testing.T) {
	cfg, _, err := Parse(strings.NewReader("ipv6_prefix=2001:db8  # the node /64\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.IPv6Prefix != "2001:db8" {
		t.Errorf("IPv6Prefix = %q, want the comment stripped", cfg.IPv6Prefix)
	}
}

func TestParseWarnings(t *testing.T) {
	in := "[section]\n" +
		"ipv6_prefx=2001:db8\n" + // typo, silently ignored upstream
		"ipv6_gateway: 2001:db8::1\n" + // ':' is not a delimiter
		"domain=a.com\n" +
		"domain=b.com\n" // duplicate

	_, ps, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !has(ps, SeverityWarning, "ipv6_prefx") {
		t.Errorf("got %v, want an unknown-key warning", keys(ps))
	}
	if !has(ps, SeverityWarning, "domain") {
		t.Errorf("got %v, want a duplicate-key warning", keys(ps))
	}
	if !has(ps, SeverityWarning, "ipv6_gateway: 2001:db8::1") {
		t.Errorf("got %v, want a no-delimiter warning", keys(ps))
	}
}

func TestParseBoolStrict(t *testing.T) {
	_, ps, err := Parse(strings.NewReader("verbose=True\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !has(ps, SeverityError, "verbose") {
		t.Errorf("got %v, want an error: only \"true\" and \"false\" parse", keys(ps))
	}
}

func TestParsePartialIPv4(t *testing.T) {
	cfg, ps, err := Parse(strings.NewReader("ipv4_address=212.71.124.178\nipv4_gateway=212.71.124.177\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.IPv4 != nil {
		t.Error("partial IPv4 block kept, want dropped")
	}
	if !has(ps, SeverityError, "ipv4_address") {
		t.Errorf("got %v, want an error about the partial block", keys(ps))
	}
}

func TestRenderRoundTrip(t *testing.T) {
	want := ipv6Only()
	want.Domain = "node2.example.com"
	want.Verbose = true
	want.IPv4 = &IPv4{
		Address:      netip.MustParseAddr("212.71.124.178"),
		Gateway:      netip.MustParseAddr("212.71.124.177"),
		PrefixLength: 28,
	}

	got, ps, err := Parse(bytes.NewReader(want.Render()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ps) != 0 {
		t.Fatalf("our own output produced %v", keys(ps))
	}

	if got.IPv6Prefix != want.IPv6Prefix || got.IPv6Gateway != want.IPv6Gateway ||
		got.Domain != want.Domain || got.Verbose != want.Verbose ||
		got.NodeRewardType != want.NodeRewardType {
		t.Errorf("round trip changed scalars:\n got %+v\nwant %+v", got, want)
	}
	if got.IPv4 == nil || *got.IPv4 != *want.IPv4 {
		t.Errorf("round trip changed IPv4: got %+v, want %+v", got.IPv4, want.IPv4)
	}
}

// Rendered output must not trip the parser's own warnings, and must pass
// Validate for a config we built as valid.
func TestRenderIsValid(t *testing.T) {
	cfg, ps, err := Parse(bytes.NewReader(ipv6Only().Render()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ps) != 0 {
		t.Errorf("parse problems: %v", keys(ps))
	}
	if ps := cfg.Validate(); len(ps) != 0 {
		t.Errorf("validate problems: %v", keys(ps))
	}
}
