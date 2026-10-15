#!/bin/sh
# Install the Mixpost CLI from its GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/inovector/mixpost-cli/main/install.sh | sh
#
# Environment:
#   MIXPOST_VERSION      release to install, e.g. 0.2.0 or v0.2.0 (default: the latest)
#   MIXPOST_INSTALL_DIR  directory to install into (default: /usr/local/bin when
#                        writable, otherwise ~/.local/bin)

set -eu

REPO="inovector/mixpost-cli"
BINARY="mixpost"

say() { printf '%s\n' "$*" >&2; }
fail() { say "mixpost install: $*"; exit 1; }

need() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

detect_os() {
	case "$(uname -s)" in
	Linux) echo linux ;;
	Darwin) echo darwin ;;
	MINGW* | MSYS* | CYGWIN*) fail "on Windows, install with 'scoop install mixpost' or download the zip from https://github.com/$REPO/releases" ;;
	*) fail "unsupported operating system: $(uname -s)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	armv7l | armv7) echo armv7 ;;
	*) fail "unsupported architecture: $(uname -m)" ;;
	esac
}

latest_version() {
	# The latest release redirects to its tag, which avoids the rate-limited API.
	url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
		fail "could not reach GitHub to find the latest release"
	tag=${url##*/}
	case "$tag" in
	v*) echo "${tag#v}" ;;
	*) fail "no release has been published yet" ;;
	esac
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		fail "sha256sum or shasum is required to verify the download"
	fi
}

install_dir() {
	if [ -n "${MIXPOST_INSTALL_DIR:-}" ]; then
		echo "$MIXPOST_INSTALL_DIR"
	elif [ -w /usr/local/bin ]; then
		echo /usr/local/bin
	else
		echo "$HOME/.local/bin"
	fi
}

main() {
	need curl
	need tar
	need uname

	os=$(detect_os)
	arch=$(detect_arch)
	if [ "$os" = darwin ] && [ "$arch" = armv7 ]; then
		fail "unsupported architecture for macOS: armv7"
	fi

	version=${MIXPOST_VERSION:-}
	version=${version#v}
	[ -n "$version" ] || version=$(latest_version)

	archive="${BINARY}_${version}_${os}_${arch}.tar.gz"
	base="https://github.com/$REPO/releases/download/v$version"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "Downloading $BINARY $version for $os/$arch"
	curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "could not download $base/$archive"
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "could not download the checksums"

	expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/checksums.txt")
	[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"
	[ "$(sha256 "$tmp/$archive")" = "$expected" ] || fail "checksum mismatch for $archive"

	tar -xzf "$tmp/$archive" -C "$tmp" "$BINARY" || fail "could not unpack $archive"

	dir=$(install_dir)
	mkdir -p "$dir" || fail "could not create $dir"
	if [ -w "$dir" ]; then
		install -m 0755 "$tmp/$BINARY" "$dir/$BINARY"
	else
		say "Installing to $dir needs sudo"
		sudo install -m 0755 "$tmp/$BINARY" "$dir/$BINARY"
	fi

	say "Installed $dir/$BINARY"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "Add $dir to your PATH to run $BINARY from anywhere." ;;
	esac
	say "Next: mixpost auth login --token <personal-key>"
}

main "$@"
