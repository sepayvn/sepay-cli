#!/bin/sh
# Install the SePay CLI on macOS or Linux from GitHub Releases.
#
#   curl -fsSL https://get.sepay.vn/sepay-cli-install.sh | sh
#   curl -fsSL https://get.sepay.vn/sepay-cli-install.sh | sh -s -- --version 0.2.0 --dir /usr/local/bin
#
# Options (each overrides its environment variable):
#   --version V  SEPAY_VERSION      version to install, e.g. 0.2.0 (default: the latest release)
#   --dir D      SEPAY_INSTALL_DIR  directory for the binary (default: $HOME/.local/bin)
#   --no-modify-path  SEPAY_NO_MODIFY_PATH=1  leave the shell startup files alone
#                SEPAY_REQUIRE_SIGNATURE=1  refuse to install without cosign
#
# When the directory is not on PATH, a marked block that adds it is appended to the startup file
# of the login shell ($SHELL): ~/.zshrc (zsh, under $ZDOTDIR when set), on macOS the first of
# ~/.bash_profile, ~/.bash_login, ~/.profile that exists (bash, ~/.bash_profile when none does),
# ~/.bashrc (bash on Linux), ~/.config/fish/conf.d/sepay.fish (fish, under $XDG_CONFIG_HOME when
# set), ~/.profile (sh, dash, ksh or no $SHELL). Any other shell, or a --dir run without $HOME, only
# gets the line to add by hand; without $HOME and --dir nothing is installed.
# Running it again adds nothing; to undo, delete the lines from ">>> sepay-cli PATH >>>" to
# "<<< sepay-cli PATH <<<".
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
	say "usage: sepay-cli-install.sh [--version V] [--dir D] [--no-modify-path]"
	exit "$1"
}

# The startup file a new terminal of the user's shell reads: $SHELL, not the sh running this script.
rc_file() { # shell os
	case "$1" in
	zsh) printf '%s\n' "${ZDOTDIR:-$HOME}/.zshrc" ;;
	bash)
		if [ "$2" = darwin ]; then
			# A login bash reads only the first of these that exists; a new .bash_profile would hide
			# the other two.
			for f in .bash_profile .bash_login .profile; do
				if [ -e "$HOME/$f" ]; then
					printf '%s\n' "$HOME/$f"
					return 0
				fi
			done
			printf '%s\n' "$HOME/.bash_profile"
		else
			printf '%s\n' "$HOME/.bashrc"
		fi
		;;
	fish) printf '%s\n' "${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/sepay.fish" ;;
	"" | sh | dash | ash | ksh | mksh | yash) printf '%s\n' "$HOME/.profile" ;;
	esac
}

# Appends a marked block that puts dir on PATH to rc_file. A curl | sh child cannot change the PATH
# of the shell that ran it, hence the export to run. sepay is installed by now, so a failure here
# is a warning carrying the line to add by hand.
add_to_path() { # dir os
	hint="export PATH=\"$1:\$PATH\""
	case "$1" in
	/*) ;;
	*)
		say "warning: $1 is not an absolute path; add it to PATH by hand"
		return 0
		;;
	esac
	# The directory is written into a file the shell runs, so nothing it could expand or quote passes.
	case "$1" in
	*[\"\`\$\\:]* | *"
"*)
		say "warning: not adding $1 to PATH automatically; add it by hand: $hint"
		return 0
		;;
	esac
	# env -i and system services run without $HOME, which rc_file and the $HOME shorthand below read.
	if [ -z "${HOME:-}" ]; then
		say "HOME is not set; add $1 to your PATH by hand: $hint"
		return 0
	fi
	# Containers and cron run without $SHELL.
	shell=${SHELL:-}
	shell=${shell##*/}
	rc=$(rc_file "$shell" "$2")
	if [ -z "$rc" ]; then
		say "add $1 to your PATH in the startup file of $shell; this script edits only those of sh, bash, zsh and fish"
		return 0
	fi
	case "$1" in
	"$HOME"/*) shown="\$HOME/${1#"$HOME"/}" ;;
	*) shown=$1 ;;
	esac
	if [ "$shell" = fish ]; then
		# fish_add_path needs fish 3.2; Debian 11 and Ubuntu 20.04 ship 3.1.
		line="contains -- \"$shown\" \$PATH; or set -gx PATH \"$shown\" \$PATH"
	else
		line="export PATH=\"$shown:\$PATH\""
	fi
	begin='# >>> sepay-cli PATH >>>'
	if [ -f "$rc" ] && grep -qF "$begin" "$rc" 2>/dev/null; then
		if grep -qxF "$line" "$rc"; then
			say "$rc already adds $1 to PATH; open a new terminal, or run: $hint"
		else
			say "warning: the sepay-cli PATH block in $rc names another directory; change it to $1, or run: $hint"
		fi
		return 0
	fi
	blank=
	[ ! -s "$rc" ] || blank=yes
	if mkdir -p "${rc%/*}" 2>/dev/null && {
		[ -z "$blank" ] || echo
		printf '%s\n%s\n%s\n' "$begin" "$line" '# <<< sepay-cli PATH <<<'
	} 2>/dev/null >>"$rc"; then
		say "added $1 to PATH in $rc; open a new terminal, or run: $hint"
	else
		say "warning: could not write to $rc; add this line to it by hand: $line"
	fi
}

main() {
	version=${SEPAY_VERSION:-}
	dir=${SEPAY_INSTALL_DIR:-}
	case "${SEPAY_NO_MODIFY_PATH:-0}" in
	"" | 0) modify_path=yes ;;
	*) modify_path=no ;;
	esac
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
		--no-modify-path)
			modify_path=no
			shift
			;;
		-h | --help) usage 0 ;;
		*)
			say "unknown option: $1"
			usage 2
			;;
		esac
	done
	if [ -z "$dir" ]; then
		[ -n "${HOME:-}" ] || die "HOME is not set; pass --dir with the directory to install to"
		dir=$HOME/.local/bin
	fi

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
	*) die "unsupported OS $(uname -s); on Windows run in PowerShell: irm https://get.sepay.vn/sepay-cli-install.ps1 | iex" ;;
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
	*)
		if [ "$modify_path" = yes ]; then
			add_to_path "$dir" "$os"
		else
			say "add $dir to your PATH, e.g.: export PATH=\"$dir:\$PATH\""
		fi
		;;
	esac
	say "next: sepay login"
}

main "$@"
