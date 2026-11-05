#!/bin/sh
# Install the SePay CLI on macOS or Linux from GitHub Releases.
#
#   curl -fsSL https://get.sepay.vn/sepay-cli-install.sh | sh
#   curl -fsSL https://get.sepay.vn/sepay-cli-install.sh | sh -s -- --version 0.2.0 --dir /usr/local/bin
#
# Options (each overrides its environment variable):
#   --version V  SEPAY_VERSION      version to install, e.g. 0.2.0 (default: the latest release)
#   --dir D      SEPAY_INSTALL_DIR  directory for the binary (default: $HOME/.local/bin)
#                SEPAY_REQUIRE_SIGNATURE=1  refuse to install without cosign
#
# The archive must match its SHA-256 in checksums.txt. When cosign is on PATH, checksums.txt
# must also carry the signature of this repository's release workflow. Sends no telemetry.
# Behind a proxy, set https_proxy: curl and wget both read it.
#
# Everything runs inside main, called on the last line, so a download cut short runs nothing.
set -eu

REPO="sepayvn/sepay-cli"

say() { printf 'sepay-install: %s\n' "$*" >&2; }
die() {
	say "error: $*"
	exit 1
}
has() { command -v "$1" >/dev/null 2>&1; }

download() { # url dest
	if has curl; then
		curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 10 --retry 2 -o "$2" "$1"
	elif wget --help 2>&1 | grep -q -- --https-only; then
		wget -q --https-only -T 30 -O "$2" "$1"
	else
		# BusyBox wget (alpine) has no --https-only.
		wget -q -T 30 -O "$2" "$1"
	fi
}

sha256() {
	if has sha256sum; then
		sha256sum "$1" | awk '{ print $1 }'
	else
		shasum -a 256 "$1" | awk '{ print $1 }'
	fi
}

# The version lands in a URL and on the terminal, so only release-shaped strings pass.
is_version() {
	case "$1" in
	*[!0-9A-Za-z.-]*) return 1 ;;
	[0-9]*.[0-9]*.[0-9]*) return 0 ;;
	*) return 1 ;;
	esac
}

usage() {
	say "usage: sepay-cli-install.sh [--version V] [--dir D]"
	exit "$1"
}

main() {
	version=${SEPAY_VERSION:-}
	dir=${SEPAY_INSTALL_DIR:-}
	while [ $# -gt 0 ]; do
		case "$1" in
		--version)
			[ $# -ge 2 ] || usage 2
			version=$2
			shift 2
			;;
		--version=*)
			version=${1#*=}
			shift
			;;
		--dir)
			[ $# -ge 2 ] || usage 2
			dir=$2
			shift 2
			;;
		--dir=*)
			dir=${1#*=}
			shift
			;;
		-h | --help) usage 0 ;;
		*)
			say "unknown option: $1"
			usage 2
			;;
		esac
	done
	dir=${dir:-$HOME/.local/bin}

	has curl || has wget || die "need curl or wget"
	if ! has curl && ! wget --help 2>&1 | grep -q -- --https-only; then
		say "warning: BusyBox wget cannot refuse a redirect to plain http and may not check TLS certificates; install curl, or cosign so the signature is checked"
	fi
	case "${SEPAY_REQUIRE_SIGNATURE:-0}" in
	"" | 0) ;;
	*) has cosign || die "SEPAY_REQUIRE_SIGNATURE is set but cosign is not on PATH; install cosign (https://docs.sigstore.dev) or unset it" ;;
	esac
	has sha256sum || has shasum || die "need sha256sum or shasum to verify the download"
	for tool in tar mktemp awk; do
		has "$tool" || die "need $tool"
	done

	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "unsupported OS $(uname -s); on Windows use the Scoop bucket https://github.com/sepayvn/scoop-sepay-cli or run: npm install -g sepay-cli" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported architecture $(uname -m); supported: x86_64, arm64" ;;
	esac

	tmp=$(mktemp -d) || die "cannot create a temporary directory"
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	if [ -n "$version" ]; then
		version=${version#v}
		is_version "$version" || die "the version must look like 0.2.0 or 0.2.0-rc.1"
	else
		# The latest release's own checksums.txt names its archives, so no API call (and no API rate
		# limit) is needed. It is only read for the version; the one verified below comes from that tag.
		download "https://github.com/$REPO/releases/latest/download/checksums.txt" "$tmp/latest.txt" ||
			die "could not find the latest release of $REPO; pass --version, e.g. --version 0.1.0"
		version=$(awk '{ print $2 }' "$tmp/latest.txt" | sed -n "s/^sepay_\(.*\)_${os}_${arch}\.tar\.gz\$/\1/p" | head -n 1)
		is_version "$version" || die "could not read the latest release of $REPO; pass --version, e.g. --version 0.1.0"
	fi

	base="https://github.com/$REPO/releases/download/v$version"
	archive="sepay_${version}_${os}_${arch}.tar.gz"
	say "installing sepay $version ($os/$arch)"

	download "$base/$archive" "$tmp/$archive" || die "could not download $base/$archive"
	download "$base/checksums.txt" "$tmp/checksums.txt" || die "could not download $base/checksums.txt"

	# The SHA-256 proves the archive matches checksums.txt; only the signature proves checksums.txt
	# came from our release workflow. Whoever can swap the archive can also delete the signature,
	# so with cosign present a missing signature stops the install.
	if has cosign; then
		if ! download "$base/checksums.txt.sig" "$tmp/checksums.txt.sig" ||
			! download "$base/checksums.txt.pem" "$tmp/checksums.txt.pem"; then
			die "release $version has no signature for checksums.txt; not installing"
		fi
		cosign verify-blob "$tmp/checksums.txt" \
			--signature "$tmp/checksums.txt.sig" \
			--certificate "$tmp/checksums.txt.pem" \
			--certificate-identity-regexp "^https://github\\.com/$REPO/\\.github/workflows/release\\.yml@refs/(heads/main|tags/v.*)\$" \
			--certificate-oidc-issuer https://token.actions.githubusercontent.com \
			>/dev/null 2>&1 ||
			die "checksums.txt is not signed by the release workflow of $REPO; not installing. Report this to support@sepay.vn"
		say "signature of checksums.txt verified"
	else
		say "warning: cosign not found, checking SHA-256 only"
	fi

	want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
	case "$want" in
	"" | *[!0-9a-f]*) die "$archive has no SHA-256 in checksums.txt; not installing" ;;
	esac
	got=$(sha256 "$tmp/$archive")
	[ "$got" = "$want" ] || die "checksum mismatch for $archive; not installing"
	say "SHA-256 verified"

	tar -xzf "$tmp/$archive" -C "$tmp" sepay || die "could not unpack $archive"
	[ -f "$tmp/sepay" ] || die "$archive holds no sepay binary"
	chmod 0755 "$tmp/sepay"
	# The first line is the version; the second is its release-notes URL.
	banner=$(SEPAY_NO_UPDATE_NOTIFIER=1 "$tmp/sepay" version) || die "the downloaded sepay does not run on this machine"
	banner=${banner%%
*}

	existing=$(command -v sepay 2>/dev/null || true)
	mkdir -p "$dir" 2>/dev/null || die "cannot create $dir; pass --dir with a directory you can write to"
	# Copy next to the target, then rename: a running sepay keeps working and no half-written file is left.
	cp "$tmp/sepay" "$dir/.sepay.new.$$" 2>/dev/null || die "cannot write to $dir; pass --dir with a directory you can write to"
	mv -f "$dir/.sepay.new.$$" "$dir/sepay"

	say "installed $banner to $dir/sepay"
	if [ -n "$existing" ] && [ "$existing" != "$dir/sepay" ]; then
		say "note: another sepay at $existing comes first on your PATH; remove it or put $dir before it"
	fi
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "add $dir to your PATH, e.g.: export PATH=\"$dir:\$PATH\"" ;;
	esac
	say "next: sepay login"
}

main "$@"
