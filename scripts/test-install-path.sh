#!/usr/bin/env bash
# Run scripts/install.sh against a fake release and check how it puts the install directory on
# PATH, then print a pass/fail table. Nothing is downloaded: a curl shim on PATH serves the fake
# release, a uname shim picks the OS, and every case gets its own empty HOME.
#
#   bash scripts/test-install-path.sh
#
# Run it as a user other than root (in a container: --user 1000), or the read-only rc case
# cannot fail to write and is reported as SKIP.

set -u
cd "$(dirname "$0")/.." || exit 1
INSTALL=$(pwd)/scripts/install.sh
WORK=$(mktemp -d)
trap 'chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"' EXIT

VERSION=9.9.9
REL=$WORK/release
FAKEBIN=$WORK/fakebin
mkdir -p "$REL" "$FAKEBIN"

# The fake sepay answers `sepay version` the way the real one does.
printf '#!/bin/sh\necho "sepay version %s"\necho "https://example.invalid/notes"\n' "$VERSION" >"$WORK/sepay"
chmod 0755 "$WORK/sepay"
for os in linux darwin; do
	tar -czf "$REL/sepay_${VERSION}_${os}_amd64.tar.gz" -C "$WORK" sepay
done
(cd "$REL" && sha256sum ./*.tar.gz | sed 's| \./| |' >checksums.txt)

# curl -fsSL ... -o DEST URL: copy the release file named by the URL's last segment.
cat >"$FAKEBIN/curl" <<EOF
#!/bin/sh
while [ \$# -gt 1 ]; do
	[ "\$1" = -o ] && dest=\$2
	shift
done
cp "$REL/\${1##*/}" "\$dest" 2>/dev/null
EOF
cat >"$FAKEBIN/uname" <<'EOF'
#!/bin/sh
case "$1" in
-s) echo "$FAKE_OS" ;;
-m) echo x86_64 ;;
*) /bin/uname "$@" ;;
esac
EOF
chmod 0755 "$FAKEBIN/curl" "$FAKEBIN/uname"

BEGIN='# >>> sepay-cli PATH >>>'
# The lines the installer writes, $HOME and $PATH unexpanded.
# shellcheck disable=SC2016
LINE='export PATH="$HOME/.local/bin:$PATH"'
# shellcheck disable=SC2016
FISHLINE='contains -- "$HOME/.local/bin" $PATH; or set -gx PATH "$HOME/.local/bin" $PATH'
END='# <<< sepay-cli PATH <<<'
pass=0
fail=0
skip=0
ROWS=()

row() { # result case note
	ROWS+=("$(printf '%-6s  %-44s  %s' "$1" "$2" "$3")")
	case "$1" in PASS) pass=$((pass + 1)) ;; FAIL) fail=$((fail + 1)) ;; SKIP) skip=$((skip + 1)) ;; esac
}

# run HOME SHELL OS [env...] -- [install args...]: runs the installer, sets $out and $code.
# SHELL - leaves the variable unset, as in a container or cron.
run() {
	local home=$1 shell=$2 os=$3
	shift 3
	local envs=()
	[ "$shell" = - ] || envs+=(SHELL="$shell")
	while [ $# -gt 0 ] && [ "$1" != -- ]; do
		envs+=("$1")
		shift
	done
	[ $# -gt 0 ] && shift
	mkdir -p "$home"
	out=$(env -i HOME="$home" FAKE_OS="$os" PATH="$FAKEBIN:/usr/bin:/bin" "${envs[@]}" \
		sh "$INSTALL" --version "$VERSION" "$@" 2>&1)
	code=$?
}

blocks() { grep -cxF "$BEGIN" "$1" 2>/dev/null || true; }

# expect_block CASE FILE LINE: the installer succeeded, wrote one marked block holding LINE to FILE,
# and said so.
expect_block() {
	local name=$1 file=$2 line=$3
	if [ "$code" != 0 ]; then
		row FAIL "$name" "exit $code: $out"
	elif [ "$(blocks "$file")" != 1 ]; then
		row FAIL "$name" "$(blocks "$file") blocks in ${file#"$WORK"/}"
	elif ! grep -A2 -xF "$BEGIN" "$file" | sed -n 2,3p | diff -q - <(printf '%s\n%s\n' "$line" "$END") >/dev/null; then
		row FAIL "$name" "block is: $(grep -A2 -xF "$BEGIN" "$file" | tr '\n' '|')"
	elif ! grep -qF "added " <<<"$out" || ! grep -qF "in $file; open a new terminal, or run: export PATH=" <<<"$out"; then
		row FAIL "$name" "no 'added' line: $out"
	else
		row PASS "$name" "${file#"$WORK"/}: $line"
	fi
}

# expect_untouched CASE HOME: the installer succeeded and created no startup file under HOME.
expect_untouched() {
	local name=$1 home=$2 files
	files=$(find "$home" -type f ! -path "*/.local/bin/*" | sed "s|^$home/||" | tr '\n' ' ')
	if [ "$code" != 0 ]; then
		row FAIL "$name" "exit $code: $out"
	elif [ -n "$files" ]; then
		row FAIL "$name" "wrote: $files"
	else
		row PASS "$name" "$(grep -oE 'add .* to your PATH.*|already adds.*' <<<"$out" | head -n 1)"
	fi
}

h=$WORK/zsh
run "$h" /bin/zsh Linux --
expect_block "zsh: ~/.zshrc" "$h/.zshrc" "$LINE"
run "$h" /bin/zsh Linux --
if [ "$code" = 0 ] && [ "$(blocks "$h/.zshrc")" = 1 ] && grep -qF "already adds" <<<"$out"; then
	row PASS "zsh: second run writes nothing" "1 block, says it is already there"
else
	row FAIL "zsh: second run writes nothing" "exit $code, $(blocks "$h/.zshrc") blocks: $out"
fi

h=$WORK/zdotdir
run "$h" /usr/bin/zsh Linux ZDOTDIR="$h/zsh" --
expect_block "zsh: \$ZDOTDIR/.zshrc" "$h/zsh/.zshrc" "$LINE"

h=$WORK/bash-mac
run "$h" /bin/bash Darwin --
expect_block "bash on macOS: ~/.bash_profile" "$h/.bash_profile" "$LINE"
[ ! -e "$h/.bashrc" ] || row FAIL "bash on macOS: ~/.bashrc untouched" "$h/.bashrc was created"

# A login bash reads only the first of ~/.bash_profile, ~/.bash_login, ~/.profile that exists, so a
# new ~/.bash_profile would hide the user's own file.
for f in .bash_login .profile; do
	h=$WORK/bash-mac$f
	mkdir -p "$h"
	echo 'export EDITOR=vim' >"$h/$f"
	run "$h" /bin/bash Darwin --
	expect_block "bash on macOS, only ~/$f: ~/$f" "$h/$f" "$LINE"
	[ ! -e "$h/.bash_profile" ] || row FAIL "bash on macOS, only ~/$f: no ~/.bash_profile" "$h/.bash_profile was created"
done

h=$WORK/bash-linux
run "$h" /bin/bash Linux --
expect_block "bash on Linux: ~/.bashrc" "$h/.bashrc" "$LINE"

h=$WORK/fish
run "$h" /usr/local/bin/fish Darwin --
expect_block "fish: conf.d/sepay.fish" "$h/.config/fish/conf.d/sepay.fish" "$FISHLINE"

# fish reads conf.d under $XDG_CONFIG_HOME when it is set, never ~/.config.
h=$WORK/fish-xdg
run "$h" /usr/bin/fish Linux XDG_CONFIG_HOME="$h/xdg" --
expect_block "fish: \$XDG_CONFIG_HOME/fish/conf.d" "$h/xdg/fish/conf.d/sepay.fish" "$FISHLINE"
[ ! -e "$h/.config" ] || row FAIL "fish: \$XDG_CONFIG_HOME, ~/.config untouched" "$h/.config was created"

h=$WORK/sh
run "$h" /bin/sh Linux --
expect_block "sh: ~/.profile" "$h/.profile" "$LINE"

h=$WORK/noshell
run "$h" - Linux --
expect_block "no \$SHELL: ~/.profile" "$h/.profile" "$LINE"

h=$WORK/tcsh
run "$h" /bin/tcsh Darwin --
if grep -qF "added " <<<"$out"; then
	row FAIL "tcsh: nothing written, no 'added'" "$out"
else
	expect_untouched "tcsh: nothing written, no 'added'" "$h"
fi

h=$WORK/emptyshell
run "$h" "" Linux --
expect_block "empty \$SHELL: ~/.profile" "$h/.profile" "$LINE"

h=$WORK/keep
mkdir -p "$h"
printf 'alias ll="ls -l"\nexport EDITOR=vim' >"$h/.zshrc" # no trailing newline
before=$(cat "$h/.zshrc")
run "$h" /bin/zsh Linux --
expect_block "existing ~/.zshrc: block appended" "$h/.zshrc" "$LINE"
if [ "$(head -n 2 "$h/.zshrc")" = "$before" ] && [ "$(sed -n 3p "$h/.zshrc")" = "$BEGIN" ]; then
	row PASS "existing ~/.zshrc: earlier lines kept" "2 lines kept, block starts on its own line"
else
	row FAIL "existing ~/.zshrc: earlier lines kept" "$(tr '\n' '|' <"$h/.zshrc")"
fi

h=$WORK/outside
run "$h" /bin/zsh Linux -- --dir "$WORK/opt/bin"
expect_block "--dir outside HOME: absolute path" "$h/.zshrc" "export PATH=\"$WORK/opt/bin:\$PATH\""

h=$WORK/onpath
mkdir -p "$h"
out=$(env -i HOME="$h" SHELL=/bin/zsh FAKE_OS=Linux PATH="$FAKEBIN:/usr/bin:/bin:$h/.local/bin" \
	sh "$INSTALL" --version "$VERSION" 2>&1)
code=$?
expect_untouched "dir already on PATH: nothing written" "$h"

h=$WORK/env-off
run "$h" /bin/zsh Linux SEPAY_NO_MODIFY_PATH=1 --
expect_untouched "SEPAY_NO_MODIFY_PATH=1: nothing written" "$h"

h=$WORK/flag-off
run "$h" /bin/zsh Linux SEPAY_NO_MODIFY_PATH=0 -- --no-modify-path
expect_untouched "--no-modify-path beats SEPAY_NO_MODIFY_PATH=0" "$h"

# Each of these could end the quoted PATH line or expand when the startup file runs.
i=0
for c in '$' '"' '`' "\\" ':' $'\n'; do
	i=$((i + 1))
	h=$WORK/char$i
	label=$c
	[ "$c" != $'\n' ] || label=newline
	run "$h" /bin/zsh Linux -- --dir "$WORK/a${c}b$i/bin"
	if [ "$code" = 0 ] && [ -z "$(find "$h" -type f)" ] && grep -qF "not adding" <<<"$out"; then
		row PASS "dir with $label in it: not written, warned" "add it by hand"
	else
		row FAIL "dir with $label in it: not written, warned" "exit $code: $out"
	fi
done

h=$WORK/relative
mkdir -p "$h"
out=$(cd "$h" && env -i HOME="$h" SHELL=/bin/zsh FAKE_OS=Linux PATH="$FAKEBIN:/usr/bin:/bin" \
	sh "$INSTALL" --version "$VERSION" --dir rel/bin 2>&1)
code=$?
if [ "$code" = 0 ] && [ -z "$(find "$h" -type f ! -path "$h/rel/bin/*")" ] && grep -qF "not an absolute path" <<<"$out"; then
	row PASS "relative --dir: not written, warned" "add it by hand"
else
	row FAIL "relative --dir: not written, warned" "exit $code: $out"
fi

# env -i and system services run without $HOME: sepay is installed and nothing names a startup file.
for shell in /bin/zsh /bin/bash /usr/bin/fish -; do
	h=$WORK/nohome${shell//\//-}
	mkdir -p "$h"
	envs=()
	[ "$shell" = - ] || envs+=(SHELL="$shell")
	out=$(env -i FAKE_OS=Linux PATH="$FAKEBIN:/usr/bin:/bin" "${envs[@]}" \
		sh "$INSTALL" --version "$VERSION" --dir "$h/bin" 2>&1)
	code=$?
	name="no \$HOME, SHELL=$shell: installed, exit 0"
	if [ "$code" = 0 ] && [ -x "$h/bin/sepay" ] && [ -z "$(find "$h" -type f ! -path "$h/bin/*")" ] &&
		grep -qF "HOME is not set" <<<"$out" && ! grep -qv '^sepay-install: ' <<<"$out"; then
		row PASS "$name" "only installer lines, no startup file"
	else
		row FAIL "$name" "exit $code: $out"
	fi
done

for home in unset empty; do
	envs=()
	[ "$home" = unset ] || envs+=(HOME=)
	out=$(cd "$WORK" && env -i FAKE_OS=Linux PATH="$FAKEBIN:/usr/bin:/bin" SHELL=/bin/zsh "${envs[@]}" \
		sh "$INSTALL" --version "$VERSION" 2>&1)
	code=$?
	name="HOME $home, no --dir: exit 1, asks for --dir"
	if [ "$code" = 1 ] && grep -qxF "sepay-install: error: HOME is not set; pass --dir with the directory to install to" <<<"$out" &&
		! grep -qv '^sepay-install: ' <<<"$out" && [ ! -e /.local/bin/sepay ]; then
		row PASS "$name" "nothing downloaded or installed"
	else
		row FAIL "$name" "exit $code: $out"
	fi
done

h=$WORK/readonly
mkdir -p "$h"
echo 'export EDITOR=vim' >"$h/.zshrc"
chmod 0444 "$h/.zshrc"
if [ "$(id -u)" = 0 ]; then
	row SKIP "read-only ~/.zshrc: installed, warned" "root writes to a 0444 file; run as another user"
else
	run "$h" /bin/zsh Linux --
	if [ "$code" = 0 ] && [ -x "$h/.local/bin/sepay" ] && grep -qF "could not write to $h/.zshrc" <<<"$out" &&
		[ "$(cat "$h/.zshrc")" = 'export EDITOR=vim' ] && [ "$(stat -c %a "$h/.zshrc")" = 444 ] &&
		! grep -qv '^sepay-install: ' <<<"$out"; then
		row PASS "read-only ~/.zshrc: installed, warned" "exit 0, file and mode unchanged, only installer lines"
	else
		row FAIL "read-only ~/.zshrc: installed, warned" "exit $code: $out"
	fi
fi

printf '%-6s  %-44s  %s\n' RESULT CASE NOTE
printf '%s\n' "${ROWS[@]}"
echo
echo "$pass passed, $fail failed, $skip skipped"
[ "$fail" = 0 ]
