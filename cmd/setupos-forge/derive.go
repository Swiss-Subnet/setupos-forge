package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"text/tabwriter"

	"github.com/swiss-subnet/setupos-forge/pkg/deterministic"
)

type derived struct {
	Role string `json:"role"`
	MAC  string `json:"mac"`
	IPv6 string `json:"ipv6"`
}

func runDerive(args []string) error {
	fs := flag.NewFlagSet("derive", flag.ContinueOnError)
	macFlag := fs.String("bmc-mac", "", "BMC (iLO/IPMI) MAC of the machine, as `ipmitool lan print` reports it")
	prefix := fs.String("prefix", "", "IPv6 prefix, in the form config.ini uses")
	env := fs.String("env", "mainnet", "deployment environment: mainnet or testnet")
	slot := fs.Uint("slot", 0, "GuestOS VM slot; 0 derives the single-guest set")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *macFlag == "" || *prefix == "" {
		fs.Usage()
		return fmt.Errorf("-bmc-mac and -prefix are required")
	}
	if *slot > 255 {
		return fmt.Errorf("-slot must be 0 to 255")
	}

	mac, err := net.ParseMAC(*macFlag)
	if err != nil {
		return fmt.Errorf("-bmc-mac: %w", err)
	}
	environment := deterministic.Environment(*env)
	if !environment.Valid() {
		return fmt.Errorf("-env must be mainnet or testnet, got %q", *env)
	}

	var out []derived
	if *slot == 0 {
		n, err := deterministic.Derive(mac, environment, *prefix)
		if err != nil {
			return err
		}
		out = []derived{
			{"SetupOS", n.SetupOS.MAC.String(), n.SetupOS.IPv6.String()},
			{"HostOS", n.HostOS.MAC.String(), n.HostOS.IPv6.String()},
			{"GuestOS", n.GuestOS.MAC.String(), n.GuestOS.IPv6.String()},
		}
	} else {
		iface, err := deterministic.Address(mac, environment, deterministic.GuestOS, deterministic.Slot(*slot), *prefix)
		if err != nil {
			return err
		}
		out = []derived{{fmt.Sprintf("GuestOS slot %d", *slot), iface.MAC.String(), iface.IPv6.String()}}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ROLE\tMAC\tIPV6")
	for _, d := range out {
		fmt.Fprintf(w, "%s\t%s\t%s\n", d.Role, d.MAC, d.IPv6)
	}
	return w.Flush()
}
