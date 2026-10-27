package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/swiss-subnet/setupos-forge/pkg/image"
	"github.com/swiss-subnet/setupos-forge/pkg/release"
)

// osImage pairs a file on the data partition with where its digest is
// published.
var osImages = []struct {
	file     string
	artifact string
	// published is the name inside that artifact's SHA256SUMS
	published string
}{
	{"guest-os.img.tar.zst", release.GuestOSDiskImg, "disk-img.tar.zst"},
	{"host-os.img.tar.zst", release.HostOSDiskImg, "disk-img.tar.zst"},
}

// verifyOSImages opens an image and checks its embedded OS images against a
// revision. Reports whether anything was actually compared, since a revision
// that publishes no matching digest verifies nothing.
func verifyOSImages(imagePath, revision string) (bool, error) {
	img, err := image.OpenRead(imagePath)
	if err != nil {
		return false, err
	}
	defer img.Close()

	data, ok := img.DataPartition()
	if !ok {
		return false, fmt.Errorf("no data partition, so there are no OS images to check")
	}
	contents, _, err := img.Contents(data)
	if err != nil {
		return false, err
	}
	return true, checkOSImages(contents, revision)
}

// checkOSImages compares the GuestOS and HostOS an image will install against
// the digests published for a revision.
func checkOSImages(files []image.FileDigest, revision string) error {
	byName := map[string]image.FileDigest{}
	for _, f := range files {
		byName[f.Name] = f
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("OS images against %s\n", revision)
	mismatch := false

	for _, oi := range osImages {
		got, present := byName[oi.file]
		if !present {
			fmt.Printf("  %-22s not on this image\n", oi.file)
			continue
		}

		sums, err := release.Sums(ctx, revision, oi.artifact)
		if errors.Is(err, release.ErrNotPublished) {
			fmt.Printf("  %-22s %s\n                         %s\n", oi.file, got.SHA256, cout.warn("no published digest to compare against"))
			continue
		}
		if err != nil {
			return err
		}

		want, ok := sums[oi.published]
		if !ok {
			fmt.Printf("  %-22s %s\n                         %s absent from SHA256SUMS\n", oi.file, got.SHA256, oi.published)
			continue
		}
		if want == got.SHA256 {
			fmt.Printf("  %-22s %s  %s\n", oi.file, got.SHA256, cout.ok("matches published"))
			continue
		}
		mismatch = true
		fmt.Printf("  %-22s %s\n    on image  %s\n    published %s\n", oi.file, cout.bad("MISMATCH"), got.SHA256, want)
	}

	if mismatch {
		fmt.Fprintln(os.Stderr, "\nan embedded OS image does not match what the revision publishes")
		return errFindings
	}
	return nil
}
