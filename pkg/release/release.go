// Package release reads the digests DFINITY publishes for an IC revision.
//
// A SetupOS image carries the GuestOS and HostOS images it will install.
// Those are the same artifacts published under their own prefixes, so their
// digests can be checked against the publisher rather than taken on trust.
package release

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const BaseURL = "https://download.dfinity.systems/ic"

// Artifacts a SetupOS image embeds, and where their digests are published.
const (
	GuestOSDiskImg = "guest-os/disk-img"
	HostOSDiskImg  = "host-os/disk-img"
)

// ErrNotPublished means the revision has no SHA256SUMS at that prefix. As of
// 2026-10 host-os/disk-img is not published, only host-os/update-img, so the
// HostOS an image carries cannot be checked against the publisher.
var ErrNotPublished = errors.New("no SHA256SUMS published for this artifact")

// Sums fetches a SHA256SUMS file and returns filename to digest.
func Sums(ctx context.Context, revision, artifact string) (map[string]string, error) {
	url := fmt.Sprintf("%s/%s/%s/SHA256SUMS", BaseURL, revision, artifact)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusForbidden:
		return nil, fmt.Errorf("%s: %w", artifact, ErrNotPublished)
	default:
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	sums, err := parseSums(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", artifact, err)
	}
	return sums, nil
}

// parseSums reads sha256sum output: "<64 hex><two spaces><filename>". The
// separator is two spaces, or a space and an asterisk in binary mode.
func parseSums(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		digest, name, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || digest == "" {
			continue
		}
		name = strings.TrimPrefix(strings.TrimSpace(name), "*")
		if name == "" {
			continue
		}
		out[name] = digest
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("SHA256SUMS was empty")
	}
	return out, nil
}
