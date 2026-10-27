package release

import (
	"strings"
	"testing"
)

func TestParseSums(t *testing.T) {
	// Verbatim from guest-os/disk-img/SHA256SUMS, two spaces.
	const real = "ab5289b018e944162797eb6e788ba1a8418e5c526972a085a589d45404037080  disk-img.tar.zst\n"

	got, err := parseSums(strings.NewReader(real))
	if err != nil {
		t.Fatalf("parseSums: %v", err)
	}
	if want := "ab5289b018e944162797eb6e788ba1a8418e5c526972a085a589d45404037080"; got["disk-img.tar.zst"] != want {
		t.Errorf("got %q, want %q", got["disk-img.tar.zst"], want)
	}
}

func TestParseSumsMultipleEntries(t *testing.T) {
	// host-os/update-img publishes two files, so picking the right name matters.
	const in = "116c920945f1fdacb130292e9ee0c7573f3e916d4705282d90fb8183f654b0d3  update-img-test.tar.zst\n" +
		"01f76db917c792e5f5554df575c4c156dbfc43f00ba6a66e57eca1c64a0e0fd5  update-img.tar.zst\n"

	got, err := parseSums(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseSums: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(got), got)
	}
	if got["update-img.tar.zst"] != "01f76db917c792e5f5554df575c4c156dbfc43f00ba6a66e57eca1c64a0e0fd5" {
		t.Errorf("update-img.tar.zst = %q", got["update-img.tar.zst"])
	}
}

func TestParseSumsBinaryMode(t *testing.T) {
	// sha256sum -b writes " *name" instead of two spaces.
	got, err := parseSums(strings.NewReader("deadbeef *disk-img.tar.zst\n"))
	if err != nil {
		t.Fatalf("parseSums: %v", err)
	}
	if got["disk-img.tar.zst"] != "deadbeef" {
		t.Errorf("got %v, want the asterisk stripped from the name", got)
	}
}

func TestParseSumsRejectsEmpty(t *testing.T) {
	for _, in := range []string{"", "\n\n", "   \n"} {
		if _, err := parseSums(strings.NewReader(in)); err == nil {
			t.Errorf("parseSums(%q) succeeded, want an error", in)
		}
	}
}
