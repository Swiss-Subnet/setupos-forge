package main

import (
	"flag"
	"fmt"
	"net/netip"
	"os"

	"github.com/swiss-subnet/setupos-forge/pkg/config"
)

func runRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	prefix := fs.String("prefix", "", "ipv6_prefix, with no \"::\"")
	gateway := fs.String("gateway", "", "ipv6_gateway")
	reward := fs.String("reward-type", "", "node_reward_type, for example type3.1")
	domain := fs.String("domain", "", "domain, required alongside IPv4")
	v4addr := fs.String("ipv4-address", "", "ipv4_address")
	v4gw := fs.String("ipv4-gateway", "", "ipv4_gateway")
	v4len := fs.Int("ipv4-prefix-length", 0, "ipv4_prefix_length, with no slash")
	verbose := fs.Bool("verbose", false, "log GuestOS console to the host terminal")
	tee := fs.Bool("trusted-execution", false, "enable_trusted_execution_environment, gen2 only")
	out := fs.String("out", "-", "write here instead of stdout")
	force := fs.Bool("force", false, "write even when validation finds errors")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := config.Config{
		IPv6Prefix:                        *prefix,
		Domain:                            *domain,
		NodeRewardType:                    *reward,
		Verbose:                           *verbose,
		EnableTrustedExecutionEnvironment: *tee,
	}

	if *gateway != "" {
		addr, err := netip.ParseAddr(*gateway)
		if err != nil {
			return fmt.Errorf("-gateway: %w", err)
		}
		cfg.IPv6Gateway = addr
	}

	if *v4addr != "" || *v4gw != "" || *v4len != 0 {
		if *v4addr == "" || *v4gw == "" || *v4len == 0 {
			return fmt.Errorf("-ipv4-address, -ipv4-gateway and -ipv4-prefix-length must be given together")
		}
		addr, err := netip.ParseAddr(*v4addr)
		if err != nil {
			return fmt.Errorf("-ipv4-address: %w", err)
		}
		gw, err := netip.ParseAddr(*v4gw)
		if err != nil {
			return fmt.Errorf("-ipv4-gateway: %w", err)
		}
		cfg.IPv4 = &config.IPv4{Address: addr, Gateway: gw, PrefixLength: *v4len}
	}

	ps := cfg.Validate()
	for _, p := range ps {
		fmt.Fprintln(os.Stderr, p)
	}
	if ps.HasErrors() && !*force {
		fmt.Fprintln(os.Stderr, "\nnot writing. Pass -force to override")
		return errFindings
	}

	if *out == "-" {
		_, err := os.Stdout.Write(cfg.Render())
		return err
	}
	return os.WriteFile(*out, cfg.Render(), 0o644)
}
