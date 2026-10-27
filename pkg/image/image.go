// Package image injects configuration into a SetupOS installer image.
//
// The config partition is found by its FAT label, not by the offset in
// partitions.csv, so a layout change fails loudly instead of corrupting an
// image.
package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/diskfs/go-diskfs/partition/gpt"
	part "github.com/diskfs/go-diskfs/partition/part"
)

// Paths on the config partition, as config_tool and check-hardware.sh expect
// to find them under /config.
const (
	ConfigLabel  = "CONFIG"
	ConfigINI    = "config.ini"
	OperatorKey  = "node_operator_private_key.pem"
	SSHKeysDir   = "ssh_authorized_keys"
	SSHAdminKeys = "ssh_authorized_keys/admin"
)

type Image struct {
	disk      *disk.Disk
	fs        filesystem.FileSystem
	Partition int // 1-indexed, as GPT numbers them
}

func Open(imagePath string) (*Image, error) { return open(imagePath, diskfs.ReadWrite) }

func OpenRead(imagePath string) (*Image, error) { return open(imagePath, diskfs.ReadOnly) }

func open(imagePath string, mode diskfs.OpenModeOption) (*Image, error) {
	d, err := diskfs.Open(imagePath, diskfs.WithOpenMode(mode))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", imagePath, err)
	}
	idx, cfs, err := findConfig(d)
	if err != nil {
		d.Close()
		return nil, err
	}
	return &Image{disk: d, fs: cfs, Partition: idx}, nil
}

func (i *Image) Close() error { return i.disk.Close() }

func findConfig(d *disk.Disk) (int, filesystem.FileSystem, error) {
	table, err := d.GetPartitionTable()
	if err != nil {
		return 0, nil, fmt.Errorf("read partition table: %w", err)
	}

	var seen []string
	for idx := 1; idx <= len(table.GetPartitions()); idx++ {
		pfs, err := d.GetFilesystem(idx)
		if err != nil {
			continue // not every partition holds a filesystem we can read
		}
		labeller, ok := pfs.(interface{ Label() string })
		if !ok {
			continue
		}
		label := strings.TrimSpace(labeller.Label())
		seen = append(seen, fmt.Sprintf("%d:%q", idx, label))
		if label == ConfigLabel {
			return idx, pfs, nil
		}
	}
	return 0, nil, fmt.Errorf("no partition labelled %s; found %s. This does not look like a SetupOS image",
		ConfigLabel, strings.Join(seen, " "))
}

func (i *Image) ReadFile(name string) ([]byte, error) {
	f, err := i.fs.OpenFile(path.Join("/", name), os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (i *Image) WriteFile(name string, data []byte) error {
	if dir := path.Dir(path.Join("/", name)); dir != "/" && dir != "." {
		if err := i.mkdirAll(dir); err != nil {
			return err
		}
	}
	f, err := i.fs.OpenFile(path.Join("/", name), os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	return f.Close()
}

func (i *Image) List() ([]string, error) { return listFS(i.fs) }

// DeploymentJSON identifies the data partition.
const DeploymentJSON = "deployment.json"

func (i *Image) DataPartition() (int, bool) {
	table, err := i.disk.GetPartitionTable()
	if err != nil {
		return 0, false
	}
	parts := table.GetPartitions()
	for idx := 1; idx <= len(parts); idx++ {
		if idx == i.Partition {
			continue
		}
		pfs, err := i.filesystemOn(idx, parts[idx-1])
		if err != nil {
			continue
		}
		if _, err := pfs.Stat("/" + DeploymentJSON); err == nil {
			return idx, true
		}
	}
	return 0, false
}

// A real release labels only the config partition and leaves the GPT names
// empty.
func (d PartitionDigest) Describe() string {
	if d.Label == "" {
		return fmt.Sprintf("partition %d", d.Index)
	}
	return fmt.Sprintf("partition %d (%s)", d.Index, d.Label)
}

type FileDigest struct {
	Partition int    `json:"partition"`
	Label     string `json:"label"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// Named partitions only, or all readable ones if none are named. A real
// release has files go-diskfs cannot open, so those come back as skipped
// rather than failing the whole walk.
func (i *Image) Contents(only ...int) (files []FileDigest, skipped []string, err error) {
	table, err := i.disk.GetPartitionTable()
	if err != nil {
		return nil, nil, err
	}
	parts := table.GetPartitions()

	wanted := map[int]bool{}
	for _, idx := range only {
		wanted[idx] = true
	}

	for idx := 1; idx <= len(parts); idx++ {
		if len(wanted) > 0 && !wanted[idx] {
			continue
		}
		pfs, err := i.filesystemOn(idx, parts[idx-1])
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("partition %d: no readable filesystem (%v)", idx, err))
			continue
		}
		names, err := listFS(pfs)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("partition %d: %v", idx, err))
			continue
		}
		for _, name := range names {
			size, sum, err := hashFile(pfs, name)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%d:%s (%v)", idx, name, err))
				continue
			}
			files = append(files, FileDigest{
				Partition: idx,
				Label:     i.labelOf(idx, parts[idx-1]),
				Name:      name,
				Size:      size,
				SHA256:    sum,
			})
		}
	}
	return files, skipped, nil
}

// filesystemOn falls back to reading ext4 directly. GetFilesystem sniffs the
// filesystem type and does not recognise the ext4 that SetupOS puts on the
// data, boot and root partitions, even though the superblock magic is there.
func (i *Image) filesystemOn(idx int, p part.Partition) (filesystem.FileSystem, error) {
	pfs, err := i.disk.GetFilesystem(idx)
	if err == nil {
		return pfs, nil
	}
	ext, extErr := ext4.Read(i.disk.Backend, p.GetSize(), p.GetStart(), i.disk.LogicalBlocksize)
	if extErr != nil {
		return nil, fmt.Errorf("%v, and reading it as ext4 failed: %v", err, extErr)
	}
	return ext, nil
}

func hashFile(pfs filesystem.FileSystem, name string) (int64, string, error) {
	f, err := pfs.OpenFile(path.Join("/", name), os.O_RDONLY)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// ReadDir follows io/fs conventions, so the root is "." and paths are
// unrooted, unlike OpenFile which wants a leading slash.
func listFS(target filesystem.FileSystem) ([]string, error) {
	var out []string
	err := fs.WalkDir(target, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func (i *Image) mkdirAll(dir string) error {
	if _, err := i.fs.Stat(dir); err == nil {
		return nil
	}
	return i.fs.Mkdir(dir)
}

type PartitionDigest struct {
	Index  int    `json:"index"`
	Label  string `json:"label"` // FAT/ext label, or the GPT partition name
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Injecting must change the config partition and nothing else. The data
// partition carries the OS images that get installed, so silent damage there
// would reach the node.
func (i *Image) Digests() ([]PartitionDigest, error) {
	table, err := i.disk.GetPartitionTable()
	if err != nil {
		return nil, fmt.Errorf("read partition table: %w", err)
	}

	parts := table.GetPartitions()
	out := make([]PartitionDigest, 0, len(parts))
	for idx := 1; idx <= len(parts); idx++ {
		h := sha256.New()
		n, err := i.disk.ReadPartitionContents(idx, h)
		if err != nil {
			return nil, fmt.Errorf("read partition %d: %w", idx, err)
		}
		out = append(out, PartitionDigest{
			Index:  idx,
			Label:  i.labelOf(idx, parts[idx-1]),
			Size:   n,
			SHA256: hex.EncodeToString(h.Sum(nil)),
		})
	}
	return out, nil
}

// The filesystem label identifies the config partition. GPT names are a
// fallback and a real release leaves them empty.
func (i *Image) labelOf(idx int, p part.Partition) string {
	if pfs, err := i.disk.GetFilesystem(idx); err == nil {
		if l, ok := pfs.(interface{ Label() string }); ok {
			if label := strings.TrimSpace(l.Label()); label != "" {
				return label
			}
		}
	}
	if g, ok := p.(*gpt.Partition); ok {
		return g.Name
	}
	return ""
}

type Layout struct {
	Index int    `json:"index"`
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

func (i *Image) Layout() ([]Layout, error) {
	table, err := i.disk.GetPartitionTable()
	if err != nil {
		return nil, err
	}
	var out []Layout
	for idx, p := range table.GetPartitions() {
		g, ok := p.(*gpt.Partition)
		if !ok {
			continue
		}
		out = append(out, Layout{Index: idx + 1, Start: g.Start, End: g.End})
	}
	return out, nil
}

type Change struct {
	What   string
	Before string
	After  string
}

func DiffPartitions(before, after []PartitionDigest) []Change {
	index := func(ds []PartitionDigest) map[int]PartitionDigest {
		m := map[int]PartitionDigest{}
		for _, d := range ds {
			m[d.Index] = d
		}
		return m
	}
	a, b := index(before), index(after)

	var out []Change
	for idx, want := range a {
		got, ok := b[idx]
		switch {
		case !ok:
			out = append(out, Change{want.Describe() + " removed", want.SHA256, "absent"})
		case got.SHA256 != want.SHA256:
			out = append(out, Change{want.Describe(), want.SHA256, got.SHA256})
		}
	}
	for idx, got := range b {
		if _, ok := a[idx]; !ok {
			out = append(out, Change{got.Describe() + " added", "absent", got.SHA256})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].What < out[j].What })
	return out
}

func DiffFiles(before, after []FileDigest) []Change {
	index := func(ds []FileDigest) map[string]FileDigest {
		m := map[string]FileDigest{}
		for _, d := range ds {
			m[fmt.Sprintf("%d:%s", d.Partition, d.Name)] = d
		}
		return m
	}
	a, b := index(before), index(after)

	var out []Change
	for key, want := range a {
		got, ok := b[key]
		switch {
		case !ok:
			out = append(out, Change{want.Name + " removed", want.SHA256, "absent"})
		case got.SHA256 != want.SHA256:
			out = append(out, Change{want.Name, want.SHA256, got.SHA256})
		}
	}
	for key, got := range b {
		if _, ok := a[key]; !ok {
			out = append(out, Change{got.Name + " added", "absent", got.SHA256})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].What < out[j].What })
	return out
}

type Payload struct {
	ConfigINI         []byte // required
	NodeOperatorKey   []byte // omitted on the gen1 HSM path
	SSHAuthorizedKeys []byte // optional
}

// Apply writes a payload onto the config partition.
//
// The prod image ships no node_operator_private_key.pem at all: defs.bzl adds
// it only when the build mode contains "dev". So that file is created rather
// than replaced.
func (i *Image) Apply(p Payload) error {
	if len(p.ConfigINI) == 0 {
		return errors.New("payload has no config.ini")
	}
	writes := []struct {
		name string
		data []byte
	}{
		{ConfigINI, p.ConfigINI},
		{OperatorKey, p.NodeOperatorKey},
		{SSHAdminKeys, p.SSHAuthorizedKeys},
	}
	for _, w := range writes {
		if len(w.data) == 0 {
			continue
		}
		if err := i.WriteFile(w.name, w.data); err != nil {
			return err
		}
	}
	return nil
}

// SHA256 is the hex digest of a file.
func SHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Verify checks an image against a digest from the release's SHA256SUMS.
// Run it before injecting anything, since injecting changes the digest.
func Verify(filePath, want string) error {
	got, err := SHA256(filePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, strings.TrimSpace(want)) {
		return fmt.Errorf("sha256 mismatch:\n  got  %s\n  want %s", got, want)
	}
	return nil
}

// Copy duplicates an image so the verified original stays untouched. The copy
// ends up holding the node operator private key, so it is owner read-write
// only.
func Copy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
