#!/bin/sh
# Install the SePay CLI on macOS or Linux from GitHub Releases.
#
#   curl -fsSL https://get.sepay.vn/sepay-cli-install.sh | sh
#
# Environment:
#   SEPAY_VERSION      version to install, e.g. 0.2.0 (default: the latest release)
#   SEPAY_INSTALL_DIR  directory for the binary (default: $HOME/.local/bin)
#
# The archive must match its SHA-256 in checksums.txt. When cosign is on PATH, checksums.txt
# must also carry the signature of this repository's release workflow. Sends no telemetry.
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

main() {
	has curl || has wget || die "need curl or wget"
	has sha256sum || has shasum || die "need sha256sum or shasum to verify the download"
	for tool in tar mktemp awk; do
		has "$tool" || die "need $tool"
	done

	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	# Windows goes to npm until the Scoop bucket carries a manifest (PUBLISH_TAPS in release.yml).
	*) die "unsupported OS $(uname -s); on Windows run: npm install -g sepay-cli" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported architecture $(uname -m); supported: x86_64, arm64" ;;
	esac

	tmp=$(mktemp -d) || die "cannot create a temporary directory"
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	if [ -n "${SEPAY_VERSION:-}" ]; then
		version=${SEPAY_VERSION#v}
		is_version "$version" || die "SEPAY_VERSION must look like 0.2.0 or 0.2.0-rc.1"
	else
		download "https://api.github.com/repos/$REPO/releases/latest" "$tmp/latest.json" ||
			die "could not ask GitHub for the latest release (no network, or over GitHub's 60 requests an hour per IP); set SEPAY_VERSION, e.g. SEPAY_VERSION=0.1.0"
		version=$(sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)
		is_version "$version" || die "could not read the latest release of $REPO; set SEPAY_VERSION, e.g. SEPAY_VERSION=0.1.0"
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
	banner=$(SEPAY_NO_UPDATE_NOTIFIER=1 "$tmp/sepay" version) || die "the downloaded sepay does not run on this machine"

	dir=${SEPAY_INSTALL_DIR:-$HOME/.local/bin}
	existing=$(command -v sepay 2>/dev/null || true)
	mkdir -p "$dir" 2>/dev/null || die "cannot create $dir; set SEPAY_INSTALL_DIR to a directory you can write to"
	# Copy next to the target, then rename: a running sepay keeps working and no half-written file is left.
	cp "$tmp/sepay" "$dir/.sepay.new.$$" 2>/dev/null || die "cannot write to $dir; set SEPAY_INSTALL_DIR to a directory you can write to"
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
