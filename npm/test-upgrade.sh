#!/usr/bin/env bash
# Upgrade cases for the npm channel. Needs a registry that already serves TWO versions.
#
#   bash npm/test-upgrade.sh http://localhost:4873 0.1.3 0.1.4
#
# Upgrading is where the "wrapper + 6 platform packages" shape is most fragile, because
# the binary is replaced in place: same path, different content. The checksum cache has
# to notice that, or it will assert a never-verified binary has been verified.

set -u
cd "$(dirname "$0")/.." || exit 1
ROOT=$(pwd)
REGISTRY=${1:?missing registry address}
FROM=${2:?missing old version}
TO=${3:?missing new version}
# The published name to install. Defaults to whatever the local build produced, so a dev
# namespace needs no edit here; pass a 4th argument if dist/ is gone.
PKG=${4:-$(node -p "require('$ROOT/dist/npm/sepay/package.json').name" 2>/dev/null)}
[ -n "$PKG" ] || { echo "Error: cannot determine the package name. Pass it as the 4th argument." >&2; exit 1; }
# The platform scope does NOT follow from the wrapper's name — with the real names the
# wrapper is unscoped (sepay-cli) while the platform packages are @sepayjsc/cli-*.
PSCOPE=${5:-$(node -p "Object.keys(require('$ROOT/dist/npm/sepay/package.json').optionalDependencies)[0].split('/')[0]" 2>/dev/null)}
[ -n "$PSCOPE" ] || { echo "Error: cannot determine the platform scope. Pass it as the 5th argument." >&2; exit 1; }

WORK=$(mktemp -d)
mkdir -p "$WORK/home"
# Same isolation as test-local.sh: the CLI's own config, the checksum cache, and any
# SEPAY_* variables from the developer's shell all stay out of this run.
export HOME=$WORK/home
export XDG_CONFIG_HOME=$WORK/home/.config
export XDG_CACHE_HOME=$WORK/cache
while read -r v; do [ -n "$v" ] && unset "$v"; done < <(env | sed -n 's/^\(SEPAY_[A-Za-z0-9_]*\)=.*/\1/p')
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0; results=()
record() { results+=("$1|$2|$3|${4:-}"); case $3 in PASS) pass=$((pass+1));; FAIL) fail=$((fail+1));; esac; }
newproj() { rm -rf "$1"; mkdir -p "$1"; cd "$1" || exit 1; npm init -y >/dev/null 2>&1; }
run() { ./node_modules/.bin/sepay version 2>&1; }
key() { command -v "$1" >/dev/null 2>&1; }

# U1 — a straight upgrade. The platform package must move too, not just the wrapper.
newproj "$WORK/u1"
npm install --ignore-scripts --registry "$REGISTRY" "$PKG@$FROM" >/dev/null 2>&1
before=$(run)
npm install --ignore-scripts --registry "$REGISTRY" "$PKG@$TO" >/dev/null 2>&1
after=$(run)
plat=$(node -p "require('./node_modules/$PSCOPE/cli-linux-x64/package.json').version" 2>/dev/null)
if [[ $before == *"$FROM"* && $after == *"$TO"* && $plat == "$TO" ]]; then
  record U1 "upgrade $FROM -> $TO, platform package follows" PASS "platform package: $plat"
else
  record U1 "upgrade $FROM -> $TO, platform package follows" FAIL "before=$before after=$after platform=$plat"
fi

# U2 — a HALF-FINISHED upgrade: the wrapper is still the old version, the binary is
# already the new one. A failed install, a dropped network, or Ctrl-C while npm is
# unpacking all produce this state. The old wrapper carries the old checksum table, so
# it MUST STOP.
#
# The case deliberately warms the cache first, on the old binary, and only then swaps.
# Comparing printed versions alone cannot tell "re-verified" from "trusted the cache and
# ran anyway" — both print the same line. Only this mismatched state separates them.
newproj "$WORK/u2"
npm install --ignore-scripts --registry "$REGISTRY" "$PKG@$FROM" >/dev/null 2>&1
run >/dev/null                                   # warm the cache on the OLD binary
bin=node_modules/$PSCOPE/cli-linux-x64/bin/sepay
newbin=$WORK/u2-new/node_modules/$PSCOPE/cli-linux-x64/bin/sepay
( newproj "$WORK/u2-new" >/dev/null 2>&1
  npm install --ignore-scripts --registry "$REGISTRY" "$PKG@$TO" >/dev/null 2>&1 )
cd "$WORK/u2" || exit 1
cp -p "$newbin" "$bin"                           # new binary, wrapper still the old one
out=$(run); code=$?
if [ "$code" != "0" ] && [[ $out == *"checksum verification failed"* ]]; then
  record U2 "half-finished upgrade (old wrapper + new binary) must STOP" PASS "exit $code, the cache did not hide it"
else
  record U2 "half-finished upgrade (old wrapper + new binary) must STOP" FAIL "exit=$code $out"
fi

# U3 — downgrade. Same mechanism, other direction; a cache key resting on mtime alone
# slips here, because the older file can carry back its old mtime.
newproj "$WORK/u3"
npm install --ignore-scripts --registry "$REGISTRY" "$PKG@$TO" >/dev/null 2>&1
run >/dev/null
npm install --ignore-scripts --registry "$REGISTRY" "$PKG@$FROM" >/dev/null 2>&1
out=$(run); code=$?
if [ "$code" = "0" ] && [[ $out == *"$FROM"* ]]; then
  record U3 "downgrade $TO -> $FROM" PASS "$out"
else
  record U3 "downgrade $TO -> $FROM" FAIL "exit=$code $out"
fi

# U4 — `npm update` within the semver range.
#
# `npm install pkg@x` writes a caret range, and for a 0.0.x version the caret spans
# nothing: ^0.0.1 is >=0.0.1 <0.0.2. `npm update` is then correct to stay put, so these
# two cases would be testing semver rather than this package. Skipped with the reason
# rather than reported red, because a case that is red for an unrelated reason teaches
# people to ignore red.
if [[ $FROM == 0.0.* ]]; then
  record U4 "npm update reaches $TO" SKIP "^$FROM excludes $TO — needs versions whose caret range spans both"
  record U5 "lockfile holds only the new version" SKIP "depends on U4"
else
  newproj "$WORK/u4"
  npm install --ignore-scripts --registry "$REGISTRY" "$PKG@$FROM" >/dev/null 2>&1
  npm update --ignore-scripts --registry "$REGISTRY" "$PKG" >/dev/null 2>&1
  out=$(run)
  [[ $out == *"$TO"* ]] && record U4 "npm update reaches $TO" PASS "$out" \
    || record U4 "npm update reaches $TO" FAIL "$out"

  # U5 — the lockfile: after the upgrade only the new version's hashes may remain.
  if grep -q "$TO" package-lock.json && ! grep -q "cli-linux-x64/-/cli-linux-x64-$FROM" package-lock.json; then
    record U5 "lockfile holds only the new version" PASS "$(grep -c integrity package-lock.json) integrity entries"
  else
    record U5 "lockfile holds only the new version" FAIL "$FROM still present"
  fi
fi

# U6 — npx without a version must take the newest.
cd "$WORK" || exit 1
out=$(npx --yes --registry "$REGISTRY" "$PKG" version 2>&1 | grep -E '^sepay version ' | tail -1)
[[ $out == *"$TO"* ]] && record U6 "npx with no version -> newest" PASS "$out" \
  || record U6 "npx with no version -> newest" FAIL "$out"

# U7 — a pinned version must RESIST being pulled forward. That is the only reason this
# channel is worth opening, so it gets a case of its own.
cd "$WORK" || exit 1
out=$(npx --yes --registry "$REGISTRY" "$PKG@$FROM" version 2>&1 | grep -E '^sepay version ' | tail -1)
[[ $out == *"$FROM"* ]] && record U7 "pinned @$FROM still gives $FROM although $TO exists" PASS "$out" \
  || record U7 "pinned @$FROM still gives $FROM although $TO exists" FAIL "$out"

# U8-U10 — upgrading under pnpm / yarn 1 / bun.
if key pnpm; then
  newproj "$WORK/u8"
  pnpm add "$PKG@$FROM" --registry "$REGISTRY" >/dev/null 2>&1
  pnpm add "$PKG@$TO" --registry "$REGISTRY" >/dev/null 2>&1
  out=$(run)
  [[ $out == *"$TO"* ]] && record U8 "pnpm upgrade" PASS "$out" || record U8 "pnpm upgrade" FAIL "$out"
else record U8 "pnpm upgrade" SKIP "pnpm not installed"; fi

if key yarn && [[ $(yarn --version) == 1.* ]]; then
  newproj "$WORK/u9"
  yarn add "$PKG@$FROM" --registry "$REGISTRY" --silent >/dev/null 2>&1
  yarn add "$PKG@$TO" --registry "$REGISTRY" --silent >/dev/null 2>&1
  out=$(run)
  [[ $out == *"$TO"* ]] && record U9 "Yarn 1 upgrade" PASS "$out" || record U9 "Yarn 1 upgrade" FAIL "$out"
else record U9 "Yarn 1 upgrade" SKIP "yarn 1 not installed"; fi

if key bun; then
  newproj "$WORK/u10"
  bun add "$PKG@$FROM" --registry "$REGISTRY" >/dev/null 2>&1
  bun add "$PKG@$TO" --registry "$REGISTRY" >/dev/null 2>&1
  out=$(run)
  [[ $out == *"$TO"* ]] && record U10 "bun upgrade" PASS "$out" || record U10 "bun upgrade" FAIL "$out"
else record U10 "bun upgrade" SKIP "bun not installed"; fi

cd "$ROOT" || exit 1
printf '\n%-5s %-58s %-7s %s\n' "CASE" "WHAT IT CHECKS" "RESULT" "NOTE"
printf '%.0s-' {1..120}; printf '\n'
for r in "${results[@]}"; do
  IFS='|' read -r id name verdict note <<<"$r"
  printf '%-5s %-58s %-7s %s\n' "$id" "$name" "$verdict" "$note"
done
printf '\nupgrade %s -> %s: %d passed, %d failed, %d skipped\n' \
  "$FROM" "$TO" "$pass" "$fail" "$((${#results[@]}-pass-fail))"
[ "$fail" -eq 0 ] || exit 1
