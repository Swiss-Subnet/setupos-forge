package upstream

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"
)

// These tests reach the network, so they only run when asked. CI should run
// them on a schedule rather than per commit: TestPinIntact guards against the
// pin being wrong, and TestMasterUnchanged is an early warning that upstream
// moved.
//
//	SETUPOS_FORGE_UPSTREAM=1 go test ./internal/upstream/

func networkOrSkip(t *testing.T) context.Context {
	t.Helper()
	if os.Getenv("SETUPOS_FORGE_UPSTREAM") == "" {
		t.Skip("set SETUPOS_FORGE_UPSTREAM=1 to check against dfinity/ic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The recorded digests must actually describe the pinned commit. If this
// fails, every rule in this repo is suspect, because the provenance claim is
// false.
func TestPinIntact(t *testing.T) {
	ctx := networkOrSkip(t)

	results, err := Check(ctx, Commit)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Path, r.Err)
			continue
		}
		if r.Changed {
			t.Errorf("%s digest does not match the pin:\n  recorded %s\n  actual   %s", r.Path, r.SHA256, r.Got)
		}
	}
}

// Upstream moving is not a bug, but it does mean the ported rules need
// rereading before the tool is trusted against a newer release.
func TestMasterUnchanged(t *testing.T) {
	ctx := networkOrSkip(t)

	results, err := Check(ctx, "master")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	t.Log("\n" + Summary("master", results))

	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s unreachable: %v", r.Path, r.Err)
		}
		if r.Changed {
			t.Errorf("%s changed upstream; %s mirrors its %s", r.Path, r.Mirror, r.Rules)
		}
	}
}

// Offline checks, so the manifest cannot rot unnoticed.

func TestManifestWellFormed(t *testing.T) {
	digest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(Commit) {
		t.Errorf("Commit %q is not a full git SHA", Commit)
	}
	if len(Sources) == 0 {
		t.Fatal("no sources recorded")
	}

	seen := map[string]bool{}
	for _, s := range Sources {
		if seen[s.Path] {
			t.Errorf("%s listed twice", s.Path)
		}
		seen[s.Path] = true

		if !digest.MatchString(s.SHA256) {
			t.Errorf("%s: %q is not a sha256 digest", s.Path, s.SHA256)
		}
		if s.Mirror == "" || s.Rules == "" {
			t.Errorf("%s: Mirror and Rules must say who depends on it and why", s.Path)
		}
	}
}

// Every package that ports upstream rules must be represented, or a whole
// package's worth of drift would go unwatched.
func TestAllMirrorsCovered(t *testing.T) {
	want := []string{"pkg/config", "pkg/deterministic", "pkg/image"}

	covered := map[string]bool{}
	for _, s := range Sources {
		covered[s.Mirror] = true
	}
	for _, w := range want {
		if !covered[w] {
			t.Errorf("no upstream source recorded for %s", w)
		}
	}
}
