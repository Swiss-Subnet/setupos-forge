package main

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/swiss-subnet/setupos-forge/pkg/config"
	"github.com/swiss-subnet/setupos-forge/pkg/image"
)

// showConfigDiff prints every setting the image will carry, marking the ones
// this run changes. By setting rather than by line, since the stock template
// is mostly commentary and a line diff would bury the values that differ.
func showConfigDiff(before, after []byte) {
	settings := func(b []byte) map[string]string {
		cfg, _, err := config.Parse(bytes.NewReader(b))
		if err != nil {
			return nil
		}
		return cfg.Settings()
	}
	old, new := settings(before), settings(after)

	keys := slices.Sorted(maps.Keys(old))
	keys = append(keys, slices.Sorted(maps.Keys(new))...)
	slices.Sort(keys)
	keys = slices.Compact(keys)

	fmt.Printf("\n%s\n", image.ConfigINI)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, k := range keys {
		o, n := old[k], new[k]
		if o == n {
			fmt.Fprintf(w, "  %s\t%s\t\t%s\n", k, n, cout.dim("unchanged"))
			continue
		}
		fmt.Fprintf(w, "  %s\t%s\t-> \t%s\n", k, orUnset(o), cout.bold(orUnset(n)))
	}
	w.Flush()
}

// showKeysDiff lists every key the image will carry, marking added, removed
// and kept. An authorized_keys file is a set of lines, so that is the shape.
func showKeysDiff(before, after []byte) {
	lines := func(b []byte) []string {
		var out []string
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
		return out
	}
	old, new := lines(before), lines(after)

	fmt.Printf("\n%s\n", image.SSHAdminKeys)
	if len(old) == 0 && len(new) == 0 {
		fmt.Println("  no keys on either side")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, l := range old {
		if !slices.Contains(new, l) {
			fmt.Fprintf(w, "  %s\t%s\n", cout.bad("removed"), abbreviate(l))
		}
	}
	for _, l := range new {
		if slices.Contains(old, l) {
			fmt.Fprintf(w, "  %s\t%s\n", cout.dim("kept"), abbreviate(l))
			continue
		}
		fmt.Fprintf(w, "  %s\t%s\n", cout.bold("added"), abbreviate(l))
	}
	w.Flush()
}

func orUnset(v string) string {
	if v == "" {
		return "(unset)"
	}
	return v
}

// abbreviate keeps a key line to one screen line, since the body carries no
// information a reader can check by eye.
func abbreviate(line string) string {
	const max = 72
	if len(line) <= max {
		return line
	}
	return line[:max-3] + "..."
}
