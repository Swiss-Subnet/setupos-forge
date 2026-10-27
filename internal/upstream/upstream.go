// Package upstream records which dfinity/ic sources this tool mirrors, and
// checks whether they still say what they said when the rules were ported.
//
// Everything here is a Go reimplementation of behaviour that lives upstream.
// That is a liability: a mirror that silently encodes last year's rules is
// worse than no mirror, because people trust it. Pinning each file by digest
// turns drift into a visible failure instead of a wrong answer.
package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Commit is the dfinity/ic revision every rule in this repo was read from.
const Commit = "b9f8bbf07a1aca5212ff30c7690c08d6ceeb9566"

const rawURL = "https://raw.githubusercontent.com/dfinity/ic/%s/%s"

// Source is one mirrored upstream file.
type Source struct {
	Path   string // path within dfinity/ic
	SHA256 string // digest at Commit
	Mirror string // where its rules live here
	Rules  string // what we take from it
}

// Sources is every upstream file this tool depends on. Adding a rule that
// reads from a new upstream file means adding it here too, or the drift check
// will not cover it.
var Sources = []Source{
	{
		Path:   "rs/ic_os/networking/deterministic_ips/src/lib.rs",
		SHA256: "92c3d017dfa855c18dd14b090d8714c13cd47b21f2ca33e9f56395022cdb13b0",
		Mirror: "pkg/deterministic",
		Rules:  "MAC seeding, EUI-64, prefix parsing",
	},
	{
		Path:   "rs/ic_os/networking/deterministic_ips/src/node_type.rs",
		SHA256: "27d268194ed725e5011527e6f1d4a72dc4209948fc909427ea9b6ffdbf83ee57",
		Mirror: "pkg/deterministic",
		Rules:  "node type index bytes",
	},
	{
		Path:   "rs/ic_os/config/tool/src/setupos/config_ini.rs",
		SHA256: "75bd79d6bc55275a130e5377df7178a7d6c1f44e7c9c8fc289307cfd05d5903d",
		Mirror: "pkg/config",
		Rules:  "key set, types, prefix rules, parser settings",
	},
	{
		Path:   "rs/ic_os/config/tool/src/main.rs",
		SHA256: "2c27f130476eb784f20e2b40438852e304e2c29088100ab1b82f2af0edd6c129",
		Mirror: "pkg/config",
		Rules:  "reward type pattern, IPv4 grouping",
	},
	{
		Path:   "ic-os/components/setupos/check-hardware.sh",
		SHA256: "818ea16b4e43b6283b9ec3f7d62a91c6767ffa67dada2364bca6338b3b954f3d",
		Mirror: "pkg/config",
		Rules:  "reward type to hardware generation, SEV-SNP gate",
	},
	{
		Path:   "ic-os/components/setupos/check-network.sh",
		SHA256: "7e88934b628e0f4a4ef6f3d7e5b870935985b06c5a6484b384f2b0d4ffa0ee6c",
		Mirror: "pkg/config",
		Rules:  "domain syntax, gateway reachability",
	},
	{
		Path:   "ic-os/setupos/partitions.csv",
		SHA256: "c999d531e644ad17534b93da134934f067b7cc253e3dac82017a1936731b1dd4",
		Mirror: "pkg/image",
		Rules:  "partition layout",
	},
	{
		Path:   "ic-os/setupos/defs.bzl",
		SHA256: "19391317e5df50f5b6f9146e551e0837af9ac7770d17c73d5a6ff0531eeff675",
		Mirror: "pkg/image",
		Rules:  "CONFIG label, which files each image variant ships",
	},
}

// Result is the outcome of checking one source against a git ref.
type Result struct {
	Source
	Got     string // digest at the ref
	Changed bool
	Err     error
}

// Check fetches every source at ref and compares it to the recorded digest.
//
// Against Commit, a mismatch means the pin itself is not what it claims, so
// the recorded digests cannot be trusted. Against a moving ref such as
// "master", a mismatch means upstream moved and the ported rules need
// rereading.
func Check(ctx context.Context, ref string) ([]Result, error) {
	results := make([]Result, len(Sources))
	var wg sync.WaitGroup

	for i, src := range Sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := fetchDigest(ctx, ref, src.Path)
			results[i] = Result{
				Source:  src,
				Got:     got,
				Changed: err == nil && got != src.SHA256,
				Err:     err,
			}
		}()
	}
	wg.Wait()

	sort.Slice(results, func(a, b int) bool { return results[a].Path < results[b].Path })
	return results, nil
}

func fetchDigest(ctx context.Context, ref, path string) (string, error) {
	url := fmt.Sprintf(rawURL, ref, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// A 404 is itself drift: the file moved or was deleted upstream.
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Summary renders results as a report.
func Summary(ref string, results []Result) string {
	var b strings.Builder
	var changed, failed int

	fmt.Fprintf(&b, "pinned at %s\nchecked against %s\n\n", Commit, ref)
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
			fmt.Fprintf(&b, "  ERROR   %s\n          %v\n", r.Path, r.Err)
		case r.Changed:
			changed++
			fmt.Fprintf(&b, "  CHANGED %s\n          mirrored by %s: %s\n          was %s\n          now %s\n",
				r.Path, r.Mirror, r.Rules, r.SHA256, r.Got)
		default:
			fmt.Fprintf(&b, "  ok      %s\n", r.Path)
		}
	}

	fmt.Fprintf(&b, "\n%d unchanged, %d changed, %d unreachable\n",
		len(results)-changed-failed, changed, failed)
	if changed > 0 {
		fmt.Fprintf(&b, "\nReread the changed files before trusting this tool against a newer\nIC-OS release, then update internal/upstream and UPSTREAM.md.\n")
	}
	return b.String()
}
