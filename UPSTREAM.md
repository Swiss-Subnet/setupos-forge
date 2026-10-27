# Upstream

This tool is a mirror of behaviour that lives in [dfinity/ic](https://github.com/dfinity/ic).
It does not call IC-OS code. It reimplements the rules in Go, which means it
can drift, and a tool that silently encodes last year's rules is worse than no
tool at all.

Pinned commit: `b9f8bbf07a1aca5212ff30c7690c08d6ceeb9566`

## Mirrored files

| Upstream path | What we take from it | Ours |
|---|---|---|
| `rs/ic_os/networking/deterministic_ips/src/lib.rs` | MAC seeding, EUI-64, prefix parsing | `pkg/deterministic` |
| `rs/ic_os/networking/deterministic_ips/src/node_type.rs` | node type index bytes | `pkg/deterministic` |
| `rs/ic_os/config/tool/src/setupos/config_ini.rs` | key set, types, prefix rules | `pkg/config` |
| `rs/ic_os/config/tool/src/main.rs` | reward type pattern, IPv4 grouping | `pkg/config` |
| `ic-os/components/setupos/check-hardware.sh` | reward type to hardware generation, SEV-SNP gate | `pkg/config` |
| `ic-os/components/setupos/check-network.sh` | domain syntax, gateway reachability | `pkg/config` |
| `ic-os/setupos/partitions.csv` | partition layout | `pkg/image` |
| `ic-os/setupos/defs.bzl` | what each image variant already ships | `pkg/image` |

## Test vectors

Every expected value in `pkg/deterministic` is copied from the upstream
`#[cfg(test)]` block, not computed here. If the derivation ever changes
upstream, those tests fail rather than quietly agreeing with themselves. The
same holds for `TestValidIPv6Prefix` and `TestParseUpstreamExample` in
`pkg/config`.

Cases we added on top are marked in comments. They mostly pin places where two
upstream checks disagree, such as `node_reward_type`: `main.rs` accepts
`^type[0-9]+(\.[0-9])?$` while `check-hardware.sh` accepts `(\.[0-9]+)?` and
only the type0/1/3/4 families. A value like `type4.10` passes one and fails the
other.

## Keeping the pin honest

`just test-upstream` refetches each file above at the pinned commit and at
`master`, and fails when they differ. Run it when bumping the IC release this
supports. Bumping the pin means rereading the diff and updating the rules, not
just the hash.

It is deliberately not on a schedule. dfinity/ic is busy enough that a
periodic check against `master` would fail on unrelated changes, and a check
that fails routinely stops being read.

## Parser semantics

`config_ini.rs` builds its map with the `configparser` crate, overriding only
`case_sensitive = true` and `delimiters = ['=']`. Everything else is crate
default, which is load-bearing:

- the whole line is trimmed, then keys and values are trimmed again
- a leading `;` or `#` comments the line out
- inline comments are **on**, so the first `;` or `#` anywhere truncates the line
- `:` is not a delimiter, so `key: value` parses as a key with an empty value
- sections are flattened into one map, so a key repeated across sections
  resolves nondeterministically
- the Rust side then lowercases every key, so keys are effectively case-insensitive
