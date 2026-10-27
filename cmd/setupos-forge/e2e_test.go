package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// End to end over the real binary, against an image built entirely by
// third-party tools. Our own fixtures would happily agree with our own bugs;
// sgdisk and mformat will not.
//
// Skipped when those tools are absent. `nix develop` provides them.

const (
	sectorSize   = 512
	configStart  = 411648 // ic-os/setupos/partitions.csv
	configEnd    = 514047
	configOffset = configStart * sectorSize
)

func needTools(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not installed", n)
		}
	}
}

func run(t *testing.T, name string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if !errors.As(err, &exit) {
			t.Fatalf("%s %v: %v", name, args, err)
		}
		code = exit.ExitCode()
	}
	return string(out), code
}

// buildBinary compiles the CLI under test.
func buildBinary(t *testing.T) string {
	t.Helper()
	needTools(t, "go")
	bin := filepath.Join(t.TempDir(), "setupos-forge")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// setupOSImage builds a GPT image shaped like a SetupOS installer, using only
// sgdisk and mformat.
func setupOSImage(t *testing.T, dir string) string {
	t.Helper()
	img := filepath.Join(dir, "disk.img")

	f, err := os.Create(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(300 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if out, err := exec.Command("sgdisk", "-a", "1",
		"-n", "1:2048:206847", "-t", "1:EF00", "-c", "1:esp",
		"-n", "2:206848:411647", "-t", "2:8300", "-c", "2:grub",
		"-n", "3:411648:514047", "-t", "3:0700", "-c", "3:config",
		img).CombinedOutput(); err != nil {
		t.Fatalf("sgdisk: %v\n%s", err, out)
	}

	at := img + "@@" + strconv.Itoa(configOffset)
	if out, err := exec.Command("mformat", "-i", at, "-F", "-v", "CONFIG",
		"-T", strconv.Itoa(configEnd-configStart+1), "::").CombinedOutput(); err != nil {
		t.Fatalf("mformat: %v\n%s", err, out)
	}
	return img
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodConfig = "ipv6_prefix=2001:db8:1:1\nipv6_gateway=2001:db8:1:1::1\nnode_reward_type=type4.2\n"

func TestE2EBuildThenInspect(t *testing.T) {
	needTools(t, "sgdisk", "mformat", "mtype")
	bin := buildBinary(t)
	dir := t.TempDir()

	img := setupOSImage(t, dir)
	cfg := writeFile(t, dir, "config.ini", goodConfig)
	key := writeFile(t, dir, "operator.pem", "-----BEGIN EC PRIVATE KEY-----\nnotreal\n-----END EC PRIVATE KEY-----\n")
	// Opaque bytes to the tool, which copies the file verbatim and never
	// parses it. Upstream's shipped ssh_authorized_keys/admin is comments only.
	ssh := writeFile(t, dir, "authorized_keys", "# placeholder, no key here\n")

	sum, code := run(t, bin, "inspect", "-image", img)
	if code != 1 {
		t.Fatalf("inspect on a template image should report findings, got %d:\n%s", code, sum)
	}

	out, code := run(t, bin, "build", "-image", img, "-config", cfg, "-operator-key", key, "-ssh-keys", ssh)
	if code != 0 {
		t.Fatalf("build failed (%d):\n%s", code, out)
	}
	if !strings.Contains(out, "partition 3 (CONFIG)") {
		t.Errorf("build did not report the config partition:\n%s", out)
	}

	out, code = run(t, bin, "inspect", "-image", img)
	if code != 0 {
		t.Fatalf("inspect after build failed (%d):\n%s", code, out)
	}
	for _, want := range []string{"config.ini", "node_operator_private_key.pem", "ssh_authorized_keys/admin", "node_reward_type=type4.2"} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect output missing %q:\n%s", want, out)
		}
	}

	// mtools must agree about what landed on the partition.
	got, code := run(t, "mtype", "-i", img+"@@"+strconv.Itoa(configOffset), "::config.ini")
	if code != 0 || got != goodConfig {
		t.Errorf("mtype read %q (exit %d), want %q", got, code, goodConfig)
	}
}

// SetupOS does not enforce the operator key, so we do. Omitting it has to be
// a decision, since a node without it installs fine and then cannot register.
func TestE2ERequiresOperatorKey(t *testing.T) {
	needTools(t, "sgdisk", "mformat")
	bin := buildBinary(t)
	dir := t.TempDir()

	img := setupOSImage(t, dir)
	cfg := writeFile(t, dir, "config.ini", goodConfig)

	out, code := run(t, bin, "build", "-image", img, "-config", cfg)
	if code == 0 {
		t.Fatalf("build without -operator-key succeeded:\n%s", out)
	}
	if !strings.Contains(out, "-operator-key is required") {
		t.Errorf("want the key requirement, got:\n%s", out)
	}

	out, code = run(t, bin, "build", "-image", img, "-config", cfg, "-hsm")
	if code != 0 {
		t.Fatalf("-hsm should allow the build (%d):\n%s", code, out)
	}

	key := writeFile(t, dir, "operator.pem", "not a real key\n")
	out, code = run(t, bin, "build", "-image", img, "-config", cfg, "-hsm", "-operator-key", key)
	if code == 0 {
		t.Errorf("-hsm with -operator-key should be rejected:\n%s", out)
	}
}

func TestE2ERejectsBadDigest(t *testing.T) {
	needTools(t, "sgdisk", "mformat")
	bin := buildBinary(t)
	dir := t.TempDir()

	img := setupOSImage(t, dir)
	cfg := writeFile(t, dir, "config.ini", goodConfig)

	out, code := run(t, bin, "build", "-image", img, "-config", cfg, "-hsm", "-sha256", strings.Repeat("ab", 32))
	if code == 0 {
		t.Fatalf("build accepted a wrong digest:\n%s", out)
	}
	if !strings.Contains(out, "sha256 mismatch") {
		t.Errorf("want a digest mismatch, got:\n%s", out)
	}

	// The image must be untouched after a failed verification.
	after, _ := run(t, bin, "inspect", "-image", img)
	if strings.Contains(after, "node_reward_type=type4.2") {
		t.Errorf("build wrote despite the digest mismatch:\n%s", after)
	}
}

func TestE2ERefusesInvalidConfig(t *testing.T) {
	needTools(t, "sgdisk", "mformat")
	bin := buildBinary(t)
	dir := t.TempDir()

	img := setupOSImage(t, dir)
	// type2.1 maps to no hardware generation, so check-hardware.sh would halt.
	cfg := writeFile(t, dir, "config.ini", "ipv6_prefix=2001:db8:1:1\nipv6_gateway=2001:db8:1:1::1\nnode_reward_type=type2.1\n")

	out, code := run(t, bin, "build", "-image", img, "-config", cfg, "-hsm")
	if code != 1 {
		t.Fatalf("build should refuse an invalid config, got %d:\n%s", code, out)
	}
	if !strings.Contains(out, "not injecting") {
		t.Errorf("want a refusal, got:\n%s", out)
	}

	out, _ = run(t, bin, "build", "-image", img, "-config", cfg, "-hsm", "-force")
	if !strings.Contains(out, "partition 3 (CONFIG)") {
		t.Errorf("-force should override, got:\n%s", out)
	}
}
