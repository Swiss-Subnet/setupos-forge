package image

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Injecting changes the image, so its digest no longer matches the published
// SHA256SUMS. A manifest records the source digest and a digest per partition
// so the image can still be checked later.

const manifestVersion = 1

// Manifest records an image as setupos-forge left it.
type Manifest struct {
	Version   int       `json:"version"`
	Tool      string    `json:"tool"`
	Created   time.Time `json:"created"`
	Source    Source    `json:"source"`
	Result    Result    `json:"result"`
	ConfigIdx int       `json:"config_partition"`

	Partitions []PartitionDigest `json:"partitions"`
	Layout     []Layout          `json:"layout"`
	Files      []string          `json:"config_files"`
}

// Source is the release image before anything was written to it.
type Source struct {
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Verified bool   `json:"verified"` // checked against a digest the caller supplied
}

// Result is the image after injection.
type Result struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Snapshot captures the current state of an open image.
func (i *Image) Snapshot(src Source, resultPath string) (*Manifest, error) {
	digests, err := i.Digests()
	if err != nil {
		return nil, err
	}
	layout, err := i.Layout()
	if err != nil {
		return nil, err
	}
	files, err := i.List()
	if err != nil {
		return nil, err
	}
	sum, err := SHA256(resultPath)
	if err != nil {
		return nil, err
	}

	return &Manifest{
		Version:    manifestVersion,
		Tool:       "setupos-forge",
		Created:    time.Now().UTC(),
		Source:     src,
		Result:     Result{Name: filepath.Base(resultPath), SHA256: sum},
		ConfigIdx:  i.Partition,
		Partitions: digests,
		Layout:     layout,
		Files:      files,
	}, nil
}

func (m *Manifest) Write(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func ReadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.Version != manifestVersion {
		return nil, fmt.Errorf("%s is manifest version %d, this build understands %d", path, m.Version, manifestVersion)
	}
	return &m, nil
}

// Diff is one thing that no longer matches the manifest.
type Diff struct {
	What string
	Want string
	Got  string
}

func (d Diff) String() string {
	return fmt.Sprintf("%s\n    manifest %s\n    actual   %s", d.What, d.Want, d.Got)
}

// Check recomputes an image and reports everything that drifted from the
// manifest. An empty slice means the image is byte for byte what was written.
func (m *Manifest) Check(imagePath string) ([]Diff, error) {
	sum, err := SHA256(imagePath)
	if err != nil {
		return nil, err
	}

	// The per-partition walk is redundant when the whole image matches, but
	// localises the difference when it does not.
	var diffs []Diff
	if sum != m.Result.SHA256 {
		diffs = append(diffs, Diff{"whole image", m.Result.SHA256, sum})
	}

	img, err := OpenRead(imagePath)
	if err != nil {
		return nil, err
	}
	defer img.Close()

	digests, err := img.Digests()
	if err != nil {
		return nil, err
	}
	byIndex := map[int]PartitionDigest{}
	for _, d := range digests {
		byIndex[d.Index] = d
	}

	for _, want := range m.Partitions {
		got, ok := byIndex[want.Index]
		if !ok {
			diffs = append(diffs, Diff{
				fmt.Sprintf("partition %d (%s) is gone", want.Index, want.Label),
				want.SHA256, "absent",
			})
			continue
		}
		if got.SHA256 != want.SHA256 {
			what := fmt.Sprintf("partition %d (%s)", want.Index, want.Label)
			if want.Index != m.ConfigIdx {
				what += " changed, and it is not the config partition"
			} else {
				what += " changed"
			}
			diffs = append(diffs, Diff{what, want.SHA256, got.SHA256})
		}
	}
	for _, got := range digests {
		known := slices.ContainsFunc(m.Partitions, func(d PartitionDigest) bool { return d.Index == got.Index })
		if !known {
			diffs = append(diffs, Diff{
				fmt.Sprintf("partition %d (%s) is new", got.Index, got.Label),
				"absent", got.SHA256,
			})
		}
	}

	layout, err := img.Layout()
	if err != nil {
		return nil, err
	}
	if !slices.Equal(m.Layout, layout) {
		diffs = append(diffs, Diff{"partition table", fmt.Sprint(m.Layout), fmt.Sprint(layout)})
	}

	return diffs, nil
}
