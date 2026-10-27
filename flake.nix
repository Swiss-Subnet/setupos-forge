{
  description = "setupos-forge: prepare and validate IC-OS SetupOS installer images";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        default = setupos-forge;
        setupos-forge = pkgs.buildGoModule {
          pname = "setupos-forge";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-/g/B0Se28pQUhf8x4ZHwPnPzK7lOr5XfXn/Lq5srWm4=";
          subPackages = [ "cmd/setupos-forge" ];
          meta.mainProgram = "setupos-forge";
        };
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          name = "setupos-forge";

          packages = with pkgs; [
            go
            gopls
            gotools

            # mtools is not used by the tool, which writes FAT32 itself. It is
            # here to cross-check what the tool produced against an
            # independent implementation.
            mtools

            # release artifacts ship as .tar.zst
            zstd
            yq-go
            curl

            # for reading the GPT of an image by hand when something looks off,
            # and for building the demo fixture
            gptfdisk

            # debugfs reads the ext4 data partition, which go-diskfs cannot
            e2fsprogs

            just
          ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixpkgs-fmt);
    };
}
