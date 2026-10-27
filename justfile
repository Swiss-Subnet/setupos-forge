default:
    @just --list

test:
    go test ./...

# also check the ported rules against dfinity/ic. needs network.
test-upstream:
    SETUPOS_FORGE_UPSTREAM=1 go test ./... -run Upstream -v ./internal/upstream/

build:
    go build -o setupos-forge ./cmd/setupos-forge

# Walk the whole flow against a throwaway image, built by sgdisk and mformat
demo: build
    #!/usr/bin/env bash
    set -euo pipefail
    dir=$(mktemp -d)
    sf="$PWD/setupos-forge"
    trap 'echo; echo "files left in $dir"' EXIT

    step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

    dd if=/dev/zero of="$dir/disk.img" bs=1048576 count=400 status=none
    sgdisk -a 1 \
        -n 1:2048:206847   -t 1:EF00 -c 1:esp \
        -n 2:206848:411647 -t 2:8300 -c 2:grub \
        -n 3:411648:514047 -t 3:0700 -c 3:config \
        -n 4:514048:614447 -t 4:8300 -c 4:data "$dir/disk.img" >/dev/null
    mformat -i "$dir/disk.img@@210763776" -F -v CONFIG -T 102400 ::
    # Upstream makes the data partition ext4. FAT here only because mkfs.ext4
    # is not portable, and nothing in the tool cares which it is.
    mformat -i "$dir/disk.img@@263192576" -F -v DATA -T 100400 ::
    printf 'ipv6_prefix=2a00:fb01:400:44\nipv6_gateway=2a00:fb01:400:44::1\n' > "$dir/stock.ini"
    printf '# no keys\n' > "$dir/stock-keys"
    echo "stand-in guestos" > "$dir/guest-os.img.tar.zst"
    echo "stand-in hostos"  > "$dir/host-os.img.tar.zst"
    mcopy -i "$dir/disk.img@@210763776" "$dir/stock.ini" ::config.ini
    mmd   -i "$dir/disk.img@@210763776" ::ssh_authorized_keys
    mcopy -i "$dir/disk.img@@210763776" "$dir/stock-keys" ::ssh_authorized_keys/admin
    mcopy -i "$dir/disk.img@@263192576" "$dir/guest-os.img.tar.zst" "$dir/host-os.img.tar.zst" ::

    "$sf" render -prefix 2001:db8:1:1 -gateway 2001:db8:1:1::1 \
        -reward-type type4.2 -out "$dir/config.ini"
    echo "not a real key" > "$dir/operator.pem"
    printf 'ssh-ed25519 AAAAexample admin@example\n' > "$dir/authorized_keys"
    sum=$(shasum -a 256 "$dir/disk.img" | cut -d' ' -f1)

    step "what writing changes"
    "$sf" build -image "$dir/disk.img" -out "$dir/node.img" -sha256 "$sum" \
        -config "$dir/config.ini" -operator-key "$dir/operator.pem" \
        -ssh-keys "$dir/authorized_keys"

    step "what the stick will install"
    "$sf" inspect -image "$dir/node.img"

# The same against a real release. Downloads 2.5 GB the first time, cached
# after that under .release-cache.
release := "5e8370e0a84ffb783d6a9d77e0d3e90fa3914cfe"

demo-release commit=release: build
    #!/usr/bin/env bash
    set -euo pipefail
    dir="${SETUPOS_FORGE_CACHE:-$PWD/.release-cache}/{{commit}}"
    mkdir -p "$dir"
    sf="$PWD/setupos-forge"
    base="https://download.dfinity.systems/ic/{{commit}}"

    step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

    step "fetch and check the release against its published digest"
    curl -fsSL "$base/setup-os/disk-img/SHA256SUMS" > "$dir/SHA256SUMS"
    if [ ! -f "$dir/disk-img.tar.zst" ]; then
        curl -fL --progress-bar -o "$dir/disk-img.tar.zst" "$base/setup-os/disk-img/disk-img.tar.zst"
    fi
    want=$(awk '{print $1}' "$dir/SHA256SUMS")
    got=$(shasum -a 256 "$dir/disk-img.tar.zst" | cut -d' ' -f1)
    echo "want $want"
    echo "got  $got"
    [ "$want" = "$got" ] || { echo "MISMATCH, stopping"; exit 1; }
    [ -f "$dir/disk.img" ] || tar --zstd -xf "$dir/disk-img.tar.zst" -C "$dir"

    "$sf" render -prefix 2001:db8:1:1 -gateway 2001:db8:1:1::1 \
        -reward-type type4.2 -out "$dir/config.ini"
    printf 'ssh-ed25519 AAAAexample admin@example\n' > "$dir/authorized_keys"
    echo "not a real key" > "$dir/operator.pem"

    step "what writing changes"
    # No published digest covers the extracted disk.img, so -revision checks
    # the embedded GuestOS and HostOS instead.
    "$sf" build -image "$dir/disk.img" -out "$dir/node.img" -revision {{commit}} \
        -config "$dir/config.ini" -operator-key "$dir/operator.pem" \
        -ssh-keys "$dir/authorized_keys"

    step "what the stick will install, against the published digests"
    "$sf" inspect -image "$dir/node.img" -revision {{commit}}

# Cross-compile every target and write SHA256SUMS beside them. Static, since
# nothing here needs cgo, so one binary per platform runs anywhere.
targets := "linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"

dist:
    #!/usr/bin/env bash
    set -euo pipefail
    version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
    rm -rf dist && mkdir -p dist

    for t in {{targets}}; do
        os="${t%/*}" arch="${t#*/}"
        out="setupos-forge-${os}-${arch}"
        [ "$os" = windows ] && out="${out}.exe"
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
            go build -trimpath -ldflags "-s -w -X main.version=${version}" \
            -o "dist/${out}" ./cmd/setupos-forge
        echo "built ${out}"
    done

    # Same format as the digests this tool tells people to check, so
    # `shasum -a 256 -c SHA256SUMS` just works.
    cd dist && shasum -a 256 setupos-forge-* > SHA256SUMS
    echo
    cat SHA256SUMS
    echo
    shasum -a 256 -c SHA256SUMS
