package config

import (
	"bufio"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

const (
	srcConfigIni = "rs/ic_os/config/tool/src/setupos/config_ini.rs"
	srcMain      = "rs/ic_os/config/tool/src/main.rs"
)

// knownKeys is every key config_ini.rs reads. Anything else is silently
// ignored upstream, which turns a typo into a missing-parameter failure at the
// rack.
var knownKeys = map[string]bool{
	"ipv6_prefix":                          true,
	"ipv6_gateway":                         true,
	"ipv4_address":                         true,
	"ipv4_gateway":                         true,
	"ipv4_prefix_length":                   true,
	"domain":                               true,
	"verbose":                              true,
	"node_reward_type":                     true,
	"enable_trusted_execution_environment": true,
}

// Parse reads a config.ini exactly as SetupOS does and decodes it. The error
// is for I/O only; everything the file gets wrong comes back in Problems.
//
// Parse reports what upstream rejects at decode time. Run Validate on the
// result for the rules enforced later, by the install-time checks.
func Parse(r io.Reader) (Config, Problems, error) {
	raw, ps, err := parseMap(r)
	if err != nil {
		return Config{}, ps, err
	}
	cfg, decodeProblems := fromMap(raw)
	return cfg, append(ps, decodeProblems...), nil
}

// parseMap flattens an ini file the way the upstream configparser does with
// case_sensitive=true and delimiters=['=']: the whole line is trimmed, a
// leading ';' or '#' comments it out, the first ';' or '#' anywhere truncates
// the rest, sections are flattened away, and keys are lowercased.
func parseMap(r io.Reader) (map[string]string, Problems, error) {
	var ps Problems
	out := map[string]string{}

	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, ";") || strings.HasPrefix(text, "#") {
			continue
		}
		if i := strings.IndexAny(text, ";#"); i >= 0 {
			text = strings.TrimSpace(text[:i])
		}
		if text == "" || (strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]")) {
			continue
		}

		key, value := text, ""
		if i := strings.Index(text, "="); i >= 0 {
			key, value = strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+1:])
		} else {
			ps.add(SeverityWarning, text, srcConfigIni,
				"line %d has no '=' and parses as an empty value; ':' is not a delimiter here", line)
		}
		key = strings.ToLower(key)

		if _, dup := out[key]; dup {
			ps.add(SeverityWarning, key, srcConfigIni,
				"set more than once. Upstream flattens sections into one map, so which value wins is not defined")
		}
		if !knownKeys[key] {
			ps.add(SeverityWarning, key, srcConfigIni, "not a key SetupOS reads, so it is silently ignored")
		}
		out[key] = value
	}
	return out, ps, sc.Err()
}

func fromMap(m map[string]string) (Config, Problems) {
	var ps Problems
	var c Config

	c.IPv6Prefix = m["ipv6_prefix"]
	c.Domain = m["domain"]
	c.NodeRewardType = m["node_reward_type"]

	if v, ok := m["ipv6_gateway"]; ok && v != "" {
		addr, err := netip.ParseAddr(v)
		switch {
		case err != nil:
			ps.add(SeverityError, "ipv6_gateway", srcConfigIni, "%q is not an IP address", v)
		case !addr.Is6() || addr.Is4In6():
			ps.add(SeverityError, "ipv6_gateway", srcConfigIni, "%q is not an IPv6 address", v)
		default:
			c.IPv6Gateway = addr
		}
	}

	c.Verbose = readBool(m, "verbose", &ps)
	c.EnableTrustedExecutionEnvironment = readBool(m, "enable_trusted_execution_environment", &ps)

	c.IPv4, ps = readIPv4(m, ps)
	return c, ps
}

// readBool mirrors read_boolean: only the exact strings "true" and "false"
// parse, so "True", "yes" and "1" are all failures.
func readBool(m map[string]string, key string, ps *Problems) bool {
	v, ok := m[key]
	if !ok || v == "" {
		return false
	}
	switch v {
	case "true":
		return true
	case "false":
		return false
	}
	ps.add(SeverityError, key, srcConfigIni, "%q is not a boolean. Only \"true\" and \"false\" parse", v)
	return false
}

// readIPv4 mirrors main.rs: the three fields are all-or-none, and a partial
// set is dropped with nothing worse than a printed warning.
func readIPv4(m map[string]string, ps Problems) (*IPv4, Problems) {
	addr, haveAddr := m["ipv4_address"]
	gw, haveGW := m["ipv4_gateway"]
	length, haveLen := m["ipv4_prefix_length"]

	present := 0
	for _, ok := range []bool{haveAddr && addr != "", haveGW && gw != "", haveLen && length != ""} {
		if ok {
			present++
		}
	}
	if present == 0 {
		return nil, ps
	}
	if present < 3 {
		ps.add(SeverityError, "ipv4_address", srcMain,
			"only %d of ipv4_address, ipv4_gateway and ipv4_prefix_length are set; SetupOS drops a partial block and installs an IPv6-only node", present)
		return nil, ps
	}

	out := &IPv4{}
	ok := true

	for _, f := range []struct {
		key string
		raw string
		dst *netip.Addr
	}{
		{"ipv4_address", addr, &out.Address},
		{"ipv4_gateway", gw, &out.Gateway},
	} {
		parsed, err := netip.ParseAddr(f.raw)
		switch {
		case err != nil:
			ps.add(SeverityError, f.key, srcConfigIni, "%q is not an IP address", f.raw)
			ok = false
		case !parsed.Is4():
			ps.add(SeverityError, f.key, srcConfigIni, "%q is not an IPv4 address", f.raw)
			ok = false
		default:
			*f.dst = parsed
		}
	}

	n, err := strconv.Atoi(length)
	if err != nil {
		ps.add(SeverityError, "ipv4_prefix_length", srcConfigIni, "%q is not a number", length)
		ok = false
	}
	out.PrefixLength = n

	if !ok {
		return nil, ps
	}
	return out, ps
}
