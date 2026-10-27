# setupos-forge

Prepare and validate IC-OS SetupOS installer images without rebuilding them.

The official flow has you burn the USB, then hand-edit `config.ini` on the
mounted partition. Nothing checks it, and `setupos.sh` runs `check-config.sh`
before it touches a disk, so mistakes surface at the rack. This catches them
on your laptop.

```
setupos-forge derive   -bmc-mac MAC -prefix PREFIX [-env ENV] [-slot N] [-json]
setupos-forge validate [FILE]
setupos-forge render   -prefix PREFIX -gateway ADDR -reward-type TYPE [...]
setupos-forge build    -image IMG -config config.ini [-revision SHA] [-out IMG]
setupos-forge inspect  -image IMG [-revision SHA]
setupos-forge version
```

Everything exits 1 on findings, so it drops into CI as is. `just demo` walks
the whole flow against a throwaway image, `just demo-release` against a real
one.

| | |
|---|---|
| `pkg/deterministic` | derive a node's MAC and IPv6 from its BMC MAC |
| `pkg/config` | render, parse and validate `config.ini` |
| `pkg/image` | partitions, injection, digests, manifests |
| `pkg/release` | digests DFINITY publishes for a revision |

## Preparing an image

```
setupos-forge build -image disk.img -out node.img -revision <ic-commit> \
  -config config.ini -operator-key node_operator_private_key.pem
```

With `-out` the original stays untouched. `build` refuses to inject a
config.ini with errors unless you pass `-force`.

`-operator-key` is required. SetupOS does not enforce it: `check-hardware.sh`
only warns, only on gen2, and skips the check entirely for `type4`. A node
built without the key installs fine and then cannot register, so omitting it
has to be deliberate. Pass `-hsm` if the key lives on a Nitrokey instead.

## What gets written

`build` hashes every partition before and after injecting, and the files on
the config partition, then prints the difference. Only `config.ini` and the
partition holding it should appear as changed:

```
changed by this run:
  file       config.ini
  partition  partition 3 (CONFIG)
  unchanged  partition 1 ff5b9ef1...
  unchanged  partition 2 f5c835ed...
  unchanged  partition 4 9aeea1d6...
```

It also diffs `config.ini` by setting and `ssh_authorized_keys/admin` by key,
since the stock template is mostly commentary and a line diff would bury what
actually differs.

## What gets installed

The data partition carries `guest-os.img.tar.zst` and `host-os.img.tar.zst`,
the images that reach the node. `-revision` checks them against the digests
published for that IC commit:

```
OS images against 5e8370e0...
  guest-os.img.tar.zst   ab5289b0...  matches published
  host-os.img.tar.zst    d217b593...
                         no published digest to compare against
```

A mismatch refuses the build. HostOS has no published `disk-img` digest today,
only `update-img`, so it reports its digest and waits. It starts comparing
when that changes, with no code change here.

There is no published digest for the extracted `disk.img` either, only for the
`disk-img.tar.zst` it came out of. Check that archive before unpacking.
`-sha256` is for a digest you recorded yourself.

## Manifests

Injecting changes the image digest, so it no longer matches anything
published. `build` writes a manifest beside the image recording the source
digest and a digest per partition. `inspect` checks against it and reports
which partition differs.

No root, no loop devices, no mounting. The config partition is found by its
FAT label rather than by a hardcoded offset, so a layout change fails loudly
instead of corrupting an image.

## Why derivation matters

IC-OS ignores the data NIC's factory MAC. It hashes the BMC MAC, writes the
result onto the interface, and takes the address as EUI-64 over that. So every
address a node will hold is knowable before it is racked, and you can
pre-provision DNS, firewall and monitoring from it.

```go
n, _ := deterministic.Derive(bmc, deterministic.Mainnet, "2001:db8:1:1")
// n.SetupOS, n.HostOS, n.GuestOS
```

## What the validator catches

Beyond what IC-OS rejects at parse time, it flags what IC-OS accepts and then
fails on later: a reward type that maps to no hardware generation, trusted
execution on non-gen2 hardware, a gateway outside the prefix, a mistyped key
that is silently ignored, and `verbose=True` where only `true` parses.

Every finding names the upstream file that enforces it, so you can check the
claim rather than trust it.

## Provenance

A mirror of upstream behaviour, pinned by digest. `just test-upstream` checks
the pinned sources against dfinity/ic. See [UPSTREAM.md](UPSTREAM.md).

## Dependencies

Builds against a fork of `go-diskfs` that adds read support for ext4 without
the extents feature, which is what SetupOS puts on its data partition. See the
`replace` in `go.mod`.

## Development

```
nix develop
just test
just dist     # static binaries for linux, darwin and windows, plus SHA256SUMS
```

Apache-2.0.
