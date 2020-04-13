{ nixpkgs ? (fetchTarball https://releases.nixos.org/nixpkgs/nixpkgs-20.03pre212770.cc1ae9f21b9/nixexprs.tar.xz)
}:

with import nixpkgs {};

let

inherit (darwin.apple_sdk.frameworks) CoreFoundation Security;

in stdenv.mkDerivation {
  name = "shell-env";

  buildInputs = [
    git

    go
    golangci-lint
    gotools                     # guru
  ]

  # needed for building with cgo (default `go build`, `go run`, etc.)
  ++ stdenv.lib.optionals stdenv.isDarwin [ CoreFoundation Security ];

  shellHook = ''
    unset GOPATH
    export GO111MODULE=on

    PATH=$HOME/go/bin:$PATH

  '' + (if stdenv.isDarwin then ''
    # https://stackoverflow.com/questions/51161225/how-can-i-make-macos-frameworks-available-to-clang-in-a-nix-environment
    export CGO_CFLAGS="-iframework ${CoreFoundation}/Library/Frameworks -iframework ${Security}/Library/Frameworks"
    export CGO_LDFLAGS="-F ${CoreFoundation}/Library/Frameworks -F ${Security}/Library/Frameworks -framework CoreFoundation -framework Security"
  '' else "");

}
