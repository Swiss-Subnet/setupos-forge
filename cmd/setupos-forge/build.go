package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/swiss-subnet/setupos-forge/pkg/config"
	"github.com/swiss-subnet/setupos-forge/pkg/image"
)

// survey hashes every partition, plus the files on the config partition.
//
// Partition digests already cover the other partitions, and walking a real
// 2.5 GB data partition file by file would double the cost of every build for
// nothing.
func survey(path string) ([]image.PartitionDigest, []image.FileDigest, error) {
	img, err := image.OpenRead(path)
	if err != nil {
		return nil, nil, err
	}
	defer img.Close()

	parts, err := img.Digests()
	if err != nil {
		return nil, nil, err
	}
	files, _, err := img.Contents(img.Partition)
	if err != nil {
		return nil, nil, err
	}
	return parts, files, nil
}

// existingConfig reads what is already on the config partition, so the build
// can show what it replaces. Missing files are not an error: the prod image
// ships no operator key and the template may carry no SSH keys.
func existingConfig(path string) (configINI, sshKeys []byte) {
	img, err := image.OpenRead(path)
	if err != nil {
		return nil, nil
	}
	defer img.Close()

	configINI, _ = img.ReadFile(image.ConfigINI)
	sshKeys, _ = img.ReadFile(image.SSHAdminKeys)
	return configINI, sshKeys
}

func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	imagePath := fs.String("image", "", "SetupOS disk image, as unpacked from the release")
	out := fs.String("out", "", "write a modified copy here, leaving -image untouched")
	cfgPath := fs.String("config", "", "config.ini to install")
	keyPath := fs.String("operator-key", "", "node_operator_private_key.pem to install")
	hsm := fs.Bool("hsm", false, "the operator key lives on a Nitrokey HSM, so none goes on the image")
	sshPath := fs.String("ssh-keys", "", "authorized_keys file to install")
	sum := fs.String("sha256", "", "expected digest of -image, if you recorded one yourself")
	revision := fs.String("revision", "", "IC git revision, to check the embedded OS images against published digests")
	manifestPath := fs.String("manifest", "", "write the manifest here (default: alongside the image)")
	noManifest := fs.Bool("no-manifest", false, "skip the manifest")
	force := fs.Bool("force", false, "inject even when config.ini has errors")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *imagePath == "" || *cfgPath == "" {
		fs.Usage()
		return fmt.Errorf("-image and -config are required")
	}

	// SetupOS does not enforce this. check-hardware.sh only warns, only on
	// gen2, and skips the check entirely for type4. A node without the key
	// and without an HSM installs fine and then cannot register, so make
	// leaving it out a decision rather than an oversight.
	if *keyPath == "" && !*hsm {
		return fmt.Errorf("-operator-key is required. Pass -hsm if the key is on a Nitrokey instead")
	}
	if *keyPath != "" && *hsm {
		return fmt.Errorf("-operator-key and -hsm are mutually exclusive")
	}

	// Verify first. Injecting changes the digest, so afterwards there is
	// nothing left to compare against the release.
	//
	// The published SHA256SUMS covers disk-img.tar.zst, not the disk.img
	// inside it, so there is no published digest for this file. -revision
	// checks the embedded GuestOS and HostOS instead, which is the part that
	// ends up on the node.
	source := image.Source{Name: filepath.Base(*imagePath)}
	if *sum != "" {
		if err := image.Verify(*imagePath, *sum); err != nil {
			return err
		}
		source.Verified = true
		fmt.Fprintln(os.Stderr, cerr.ok("sha256 ok"))
	}
	if *revision != "" {
		verified, err := verifyOSImages(*imagePath, *revision)
		if err != nil && !errors.Is(err, errFindings) {
			return err
		}
		if errors.Is(err, errFindings) && !*force {
			fmt.Fprintln(os.Stderr, "\nnot injecting. Pass -force to override")
			return errFindings
		}
		source.Verified = source.Verified || verified
	}
	if *sum == "" && *revision == "" {
		fmt.Fprintln(os.Stderr, cerr.warn("warning: neither -sha256 nor -revision given, so nothing about this image was checked"))
	}
	sourceSum, err := image.SHA256(*imagePath)
	if err != nil {
		return err
	}
	source.SHA256 = sourceSum

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return err
	}
	cfg, ps, err := config.Parse(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	ps = append(ps, cfg.Validate()...)
	for _, p := range ps {
		fmt.Fprintln(os.Stderr, p)
	}
	if ps.HasErrors() && !*force {
		fmt.Fprintln(os.Stderr, "\nnot injecting. Fix the errors or pass -force")
		return errFindings
	}

	payload := image.Payload{ConfigINI: raw}
	for _, f := range []struct {
		path string
		dst  *[]byte
	}{
		{*keyPath, &payload.NodeOperatorKey},
		{*sshPath, &payload.SSHAuthorizedKeys},
	} {
		if f.path == "" {
			continue
		}
		data, err := os.ReadFile(f.path)
		if err != nil {
			return err
		}
		*f.dst = data
	}

	target := *imagePath
	if *out != "" {
		if err := image.Copy(*imagePath, *out); err != nil {
			return err
		}
		target = *out
	}

	// Hash everything before and after, in separate opens so nothing is read
	// out of a write buffer. This is what proves only config.ini moved.
	beforeParts, beforeFiles, err := survey(target)
	if err != nil {
		return err
	}
	oldConfig, oldKeys := existingConfig(target)

	img, err := image.Open(target)
	if err != nil {
		return err
	}
	if err := img.Apply(payload); err != nil {
		img.Close()
		return err
	}
	configIdx := img.Partition
	if err := img.Close(); err != nil {
		return err
	}

	afterParts, afterFiles, err := survey(target)
	if err != nil {
		return err
	}

	digest, err := image.SHA256(target)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s\n  partition %d (%s)\n  sha256 %s\n", target, configIdx, image.ConfigLabel, digest)

	fmt.Println(cout.bold("\nchanged by this run:"))
	for _, c := range image.DiffFiles(beforeFiles, afterFiles) {
		fmt.Printf("  file       %s\n", c.What)
	}
	for _, c := range image.DiffPartitions(beforeParts, afterParts) {
		fmt.Printf("  partition  %s\n", c.What)
	}
	for _, p := range afterParts {
		if p.Index != configIdx {
			fmt.Printf("  %s  %s %s\n", cout.dim("unchanged"), p.Describe(), cout.dim(p.SHA256))
		}
	}

	showConfigDiff(oldConfig, payload.ConfigINI)
	if payload.SSHAuthorizedKeys != nil {
		showKeysDiff(oldKeys, payload.SSHAuthorizedKeys)
	}

	if !*noManifest {
		// Reopen after the close, so the snapshot reads what actually landed
		// on disk rather than anything still buffered.
		ro, err := image.OpenRead(target)
		if err != nil {
			return err
		}
		manifest, err := ro.Snapshot(source, target)
		ro.Close()
		if err != nil {
			return err
		}
		out := *manifestPath
		if out == "" {
			out = target + ".manifest.json"
		}
		if err := manifest.Write(out); err != nil {
			return err
		}
		fmt.Printf("  manifest %s\n", out)
	}
	if *hsm {
		fmt.Fprintln(os.Stderr, "note: built for the HSM path. The Nitrokey must be plugged in during install.")
	}
	return nil
}
