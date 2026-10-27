package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/swiss-subnet/setupos-forge/pkg/config"
	"github.com/swiss-subnet/setupos-forge/pkg/image"
)

func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	imagePath := fs.String("image", "", "SetupOS disk image or USB device to read")
	manifestPath := fs.String("manifest", "", "check the image against this manifest (default: alongside the image, if present)")
	revision := fs.String("revision", "", "IC git revision, to check the embedded OS images against published digests")
	strict := fs.Bool("strict", false, "treat warnings as findings too")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *imagePath == "" {
		fs.Usage()
		return fmt.Errorf("-image is required")
	}

	digest, err := image.SHA256(*imagePath)
	if err != nil {
		return err
	}

	img, err := image.OpenRead(*imagePath)
	if err != nil {
		return err
	}
	defer img.Close()

	files, err := img.List()
	if err != nil {
		return err
	}

	digests, err := img.Digests()
	if err != nil {
		return err
	}

	fmt.Printf("%s\n  sha256    %s\n  config    partition %d (%s)\n  files     %v\n\n",
		*imagePath, digest, img.Partition, image.ConfigLabel, files)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, d := range digests {
		name := d.Label
		if name == "" {
			name = "unlabelled"
		}
		fmt.Fprintf(w, "  %d\t%s\t%d\t%s\n", d.Index, name, d.Size, d.SHA256)
	}
	w.Flush()
	fmt.Println()

	// Config and data only. Those hold what we write and which GuestOS and
	// HostOS get installed. The rest is covered by the partition digests, and
	// the root filesystem alone runs to tens of thousands of files.
	only := []int{img.Partition}
	if data, ok := img.DataPartition(); ok {
		only = append(only, data)
	}
	contents, skipped, err := img.Contents(only...)
	if err != nil {
		return err
	}
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, f := range contents {
		fmt.Fprintf(w, "  %d\t%s\t%d\t%s\n", f.Partition, f.Name, f.Size, f.SHA256)
	}
	w.Flush()
	for _, s := range skipped {
		fmt.Printf("  %s %s\n", cerr.warn("skipped:"), s)
	}
	fmt.Println()

	raw, err := img.ReadFile(image.ConfigINI)
	if err != nil {
		return fmt.Errorf("no %s on the config partition: %w", image.ConfigINI, err)
	}
	os.Stdout.Write(raw)
	fmt.Println()

	cfg, ps, err := config.Parse(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	configErr := report(append(ps, cfg.Validate()...), *strict)

	var osErr error
	if *revision != "" {
		fmt.Println()
		if osErr = checkOSImages(contents, *revision); osErr != nil && !errors.Is(osErr, errFindings) {
			return osErr
		}
	}

	// Last, so the manifest verdict is the final thing on screen rather than
	// being buried above a config.ini that happens to be fine.
	manifestErr, err := checkManifest(*imagePath, *manifestPath)
	if err != nil {
		return err
	}
	if configErr != nil {
		return configErr
	}
	if osErr != nil {
		return osErr
	}
	return manifestErr
}

// checkManifest compares the image against its manifest. A manifest the
// caller named must exist. One found by convention is a bonus, and its
// absence is only worth a note.
func checkManifest(imagePath, manifestPath string) (error, error) {
	named := manifestPath != ""
	if !named {
		manifestPath = imagePath + ".manifest.json"
	}

	m, err := image.ReadManifest(manifestPath)
	if err != nil {
		if !named && os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "no manifest at %s, so nothing to check the partitions against\n\n", manifestPath)
			return nil, nil
		}
		return nil, err
	}

	diffs, err := m.Check(imagePath)
	if err != nil {
		return nil, err
	}

	fmt.Printf("manifest %s\n  built    %s\n  from     %s (%s)\n",
		manifestPath, m.Created.Format(time.RFC3339), m.Source.Name, verifiedWord(m.Source.Verified))
	if len(diffs) == 0 {
		fmt.Printf("  state    %s\n\n", cout.ok("unchanged since it was written"))
		return nil, nil
	}

	fmt.Printf("  state    %s\n\n", cout.bad(fmt.Sprintf("%d difference(s)", len(diffs))))
	for _, d := range diffs {
		fmt.Fprintf(os.Stderr, "  %s\n", d)
	}
	fmt.Fprintln(os.Stderr)
	return errFindings, nil
}

func verifiedWord(v bool) string {
	if v {
		return "digest checked at build time"
	}
	return "digest not checked at build time"
}
