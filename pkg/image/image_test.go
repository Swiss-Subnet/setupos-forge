package image

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
)

// Sector counts from ic-os/setupos/partitions.csv at the pinned commit. The
// tests do not depend on these offsets, since the code finds the partition by
// label, but building the fixture to the real layout means a layout-sensitive
// bug would show up here.
const (
	sectorSize    = 512
	espStart      = 2048
	espSectors    = 204800
	grubStart     = 206848
	grubSectors   = 204800
	configStart   = 411648
	configSectors = 102400 // 50 MiB
	dataStart     = 514048
	// Upstream gives data 5120000 sectors (2.5 GiB) for the HostOS and
	// GuestOS images. Shrunk here, since the tests care that the partition exists
	// and stays untouched, not how big it is.
	dataSectors = 20480
)

// fixture builds a GPT image shaped like a SetupOS installer: an ESP, a
// filler partition, and a 50 MiB FAT32 partition labelled CONFIG.
func fixture(t *testing.T) string {
	t.Helper()

	imagePath := filepath.Join(t.TempDir(), "disk.img")
	size := int64((dataStart + dataSectors + 2048) * sectorSize)

	d, err := diskfs.Create(imagePath, size, diskfs.SectorSizeDefault)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	table := &gpt.Table{
		LogicalSectorSize:  sectorSize,
		PhysicalSectorSize: sectorSize,
		ProtectiveMBR:      true,
		Partitions: []*gpt.Partition{
			{Index: 1, Start: espStart, End: espStart + espSectors - 1, Type: gpt.EFISystemPartition, Name: "esp"},
			{Index: 2, Start: grubStart, End: grubStart + grubSectors - 1, Type: gpt.LinuxFilesystem, Name: "grub"},
			{Index: 3, Start: configStart, End: configStart + configSectors - 1, Type: gpt.MicrosoftBasicData, Name: "config"},
			{Index: 4, Start: dataStart, End: dataStart + dataSectors - 1, Type: gpt.LinuxFilesystem, Name: "data"},
		},
	}
	if err := d.Partition(table); err != nil {
		t.Fatalf("Partition: %v", err)
	}

	// The ESP is FAT too, so a label-blind search would find the wrong one.
	for _, spec := range []disk.FilesystemSpec{
		{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "ESP"},
		{Partition: 3, FSType: filesystem.TypeFat32, VolumeLabel: ConfigLabel},
	} {
		if _, err := d.CreateFilesystem(spec); err != nil {
			t.Fatalf("CreateFilesystem(%d): %v", spec.Partition, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return imagePath
}

func TestFindsConfigPartitionByLabel(t *testing.T) {
	img, err := Open(fixture(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer img.Close()

	if img.Partition != 3 {
		t.Errorf("Partition = %d, want 3 (the ESP is FAT32 too and must not match)", img.Partition)
	}
}

func TestApplyRoundTrip(t *testing.T) {
	path := fixture(t)

	want := Payload{
		ConfigINI: []byte("ipv6_prefix=2001:db8:1:1\nipv6_gateway=2001:db8:1:1::1\n"),
		// Both are opaque bytes to the package, which copies them verbatim.
		NodeOperatorKey:   []byte("-----BEGIN EC PRIVATE KEY-----\nnot a real key\n-----END EC PRIVATE KEY-----\n"),
		SSHAuthorizedKeys: []byte("# placeholder, no key here\n"),
	}

	img, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := img.Apply(want); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := img.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen, so the assertions read what actually landed on disk rather than
	// anything still buffered.
	img, err = OpenRead(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer img.Close()

	for _, tc := range []struct {
		name string
		want []byte
	}{
		{ConfigINI, want.ConfigINI},
		{OperatorKey, want.NodeOperatorKey},
		{SSHAdminKeys, want.SSHAuthorizedKeys},
	} {
		got, err := img.ReadFile(tc.name)
		if err != nil {
			t.Errorf("ReadFile(%s): %v", tc.name, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}

	files, err := img.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, want := range []string{ConfigINI, OperatorKey, SSHAdminKeys} {
		if !slices.Contains(files, want) {
			t.Errorf("List() = %v, missing %s", files, want)
		}
	}
}

// Replacing a file must not leave a tail of the longer original behind.
func TestWriteFileTruncates(t *testing.T) {
	path := fixture(t)

	img, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := img.WriteFile(ConfigINI, bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := img.WriteFile(ConfigINI, []byte("short\n")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	img.Close()

	img, err = OpenRead(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer img.Close()

	got, err := img.ReadFile(ConfigINI)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "short\n" {
		t.Errorf("got %d bytes %q, want the file truncated to 6", len(got), got)
	}
}

func TestApplyRejectsEmptyConfig(t *testing.T) {
	img, err := Open(fixture(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer img.Close()

	if err := img.Apply(Payload{}); err == nil {
		t.Error("Apply with no config.ini succeeded, want an error")
	}
}

func TestOpenRejectsNonSetupOSImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.img")
	size := int64(16 << 20)

	d, err := diskfs.Create(path, size, diskfs.SectorSizeDefault)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	table := &gpt.Table{
		LogicalSectorSize:  sectorSize,
		PhysicalSectorSize: sectorSize,
		ProtectiveMBR:      true,
		Partitions: []*gpt.Partition{
			{Index: 1, Start: 2048, End: uint64(size/sectorSize) - 2048, Type: gpt.MicrosoftBasicData, Name: "data"},
		},
	}
	if err := d.Partition(table); err != nil {
		t.Fatalf("Partition: %v", err)
	}
	if _, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "NOTCONFIG"}); err != nil {
		t.Fatalf("CreateFilesystem: %v", err)
	}
	d.Close()

	if _, err := Open(path); err == nil {
		t.Error("opened an image with no CONFIG partition, want an error")
	}
}

// Our FAT32 writer is only trustworthy if something else can read what it
// produced. mtools is that something else. Skipped when it is not installed.
func TestApplyReadableByMtools(t *testing.T) {
	mtype, err := exec.LookPath("mtype")
	if err != nil {
		t.Skip("mtools not installed")
	}

	path := fixture(t)
	want := []byte("ipv6_prefix=2001:db8:1:1\nipv6_gateway=2001:db8:1:1::1\n")

	img, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := img.Apply(Payload{ConfigINI: want}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := img.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// mtools reaches a filesystem inside an image with the @@offset form.
	// Its partition= drive variable is MBR only, so it is no use on GPT.
	at := fmt.Sprintf("%s@@%d", path, int64(configStart)*sectorSize)
	out, err := exec.Command(mtype, "-i", at, "::"+ConfigINI).Output()
	if err != nil {
		t.Fatalf("mtype: %v", err)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("mtools read %q, want %q", out, want)
	}
}

// The whole safety argument for injecting into a released image is that
// nothing outside the config partition moves. Partition 4 carries the HostOS
// and GuestOS images that get installed, so damage there would reach the node.
func TestApplyTouchesOnlyConfigPartition(t *testing.T) {
	path := fixture(t)

	// Put recognisable content in the partitions that must not change, so the
	// comparison is not just zeros against zeros.
	seedAt(t, path, grubStart*sectorSize, bytes.Repeat([]byte("grub"), 4096))
	seedAt(t, path, dataStart*sectorSize, bytes.Repeat([]byte("data"), 4096))

	before := digestsOf(t, path)
	layoutBefore := layoutOf(t, path)

	img, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := img.Apply(Payload{
		ConfigINI:       []byte("ipv6_prefix=2001:db8:1:1\nipv6_gateway=2001:db8:1:1::1\n"),
		NodeOperatorKey: []byte("-----BEGIN EC PRIVATE KEY-----\nnot a real key\n-----END EC PRIVATE KEY-----\n"),
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	configIdx := img.Partition
	if err := img.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	after := digestsOf(t, path)
	if len(before) != len(after) {
		t.Fatalf("partition count changed: %d then %d", len(before), len(after))
	}

	for i := range before {
		b, a := before[i], after[i]
		switch {
		case b.Index == configIdx:
			if b.SHA256 == a.SHA256 {
				t.Errorf("partition %d (%s) unchanged, but we just wrote to it", b.Index, b.Label)
			}
		case b.SHA256 != a.SHA256:
			t.Errorf("partition %d (%s) changed and must not have:\n  before %s\n  after  %s",
				b.Index, b.Label, b.SHA256, a.SHA256)
		}
		if b.Size != a.Size {
			t.Errorf("partition %d size changed: %d then %d", b.Index, b.Size, a.Size)
		}
	}

	if got := layoutOf(t, path); got != layoutBefore {
		t.Errorf("partition table moved:\n  before %s\n  after  %s", layoutBefore, got)
	}
}

// seedAt writes at a byte offset. WritePartitionContents insists on filling
// the whole partition, which is not what we want here.
func seedAt(t *testing.T, path string, offset int64, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(data, offset); err != nil {
		t.Fatalf("seed at %d: %v", offset, err)
	}
}

func digestsOf(t *testing.T, path string) []PartitionDigest {
	t.Helper()
	img, err := OpenRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	ds, err := img.Digests()
	if err != nil {
		t.Fatalf("Digests: %v", err)
	}
	return ds
}

func layoutOf(t *testing.T, path string) string {
	t.Helper()
	img, err := OpenRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	l, err := img.Layout()
	if err != nil {
		t.Fatalf("Layout: %v", err)
	}
	return fmt.Sprint(l)
}

func TestManifestRoundTripAndTamper(t *testing.T) {
	path := fixture(t)
	seedAt(t, path, dataStart*sectorSize, bytes.Repeat([]byte("data"), 4096))

	img, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := img.Apply(Payload{ConfigINI: []byte("ipv6_prefix=2001:db8:1:1\n")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	img.Close()

	ro, err := OpenRead(path)
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	m, err := ro.Snapshot(Source{Name: "disk.img", SHA256: "unused", Verified: true}, path)
	ro.Close()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	manifestPath := filepath.Join(t.TempDir(), "m.json")
	if err := m.Write(manifestPath); err != nil {
		t.Fatalf("Write: %v", err)
	}
	reread, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	diffs, err := reread.Check(path)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(diffs) != 0 {
		t.Fatalf("untouched image reported %v", diffs)
	}

	// Corrupt the data partition, which carries the HostOS and GuestOS images.
	seedAt(t, path, dataStart*sectorSize, []byte("EVIL"))

	diffs, err = reread.Check(path)
	if err != nil {
		t.Fatalf("Check after tamper: %v", err)
	}
	var sawPartition4 bool
	for _, d := range diffs {
		if strings.Contains(d.What, "partition 4") {
			sawPartition4 = true
			if !strings.Contains(d.What, "not the config partition") {
				t.Errorf("partition 4 drift should be called out as outside the config partition: %s", d.What)
			}
		}
	}
	if !sawPartition4 {
		t.Errorf("tampering with the data partition went unreported: %v", diffs)
	}
}

func TestReadManifestRejectsFutureVersion(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.json")
	if err := os.WriteFile(p, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(p); err == nil {
		t.Error("accepted a manifest from a newer version")
	}
}

func TestVerify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	if err := Verify(path, want); err != nil {
		t.Errorf("Verify: %v", err)
	}
	if err := Verify(path, strings.ToUpper(want)); err != nil {
		t.Errorf("Verify is case sensitive: %v", err)
	}
	if err := Verify(path, "deadbeef"); err == nil {
		t.Error("Verify accepted a wrong digest")
	}
}
