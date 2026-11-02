#!/usr/bin/env bash
# Run the npm-channel test cases that are possible on a Linux machine, then print a
# pass/fail table. Case 1 (macOS) and the Windows halves of cases 9-10 cannot run here;
# the script prints them as SKIP so the table never looks complete when it is not.
#
#   node npm/build-packages.js && bash npm/test-local.sh
#   node npm/build-packages.js && bash npm/test-local.sh http://localhost:4873
#
# Pass the address of a registry that already serves these 7 packages and cases 4-7
# (pnpm/yarn/bun) run; without one they are SKIP. Do not hand-wire the .tgz files into
# those tools: each has its own "overrides" shape, and testing through that shape tests
# the shape rather than how the tool handles optionalDependencies — which is the thing
# worth knowing.
#
# Never `npm install ./some-dir`: npm links the directory and leaves the 6 platform
# packages uninstalled WITHOUT SAYING SO. Every case installs from a packed .tgz.

set -u
cd "$(dirname "$0")/.." || exit 1
ROOT=$(pwd)
OUT=$ROOT/dist/npm
REGISTRY=${1:-}
WORK=$(mktemp -d)
TGZ=$WORK/tgz
mkdir -p "$TGZ" "$WORK/home"

# Isolate this run from the developer's own machine, in both directions.
#
#   HOME/XDG_CONFIG_HOME — the CLI reads credentials and config from there. The tests
#     must never read a real token, and must never write into the real profile.
#   XDG_CACHE_HOME — the checksum cache. Every run has to start from an empty cache or
#     case 12 is not measuring what it thinks it is.
#   SEPAY_* — a developer shell commonly exports SEPAY_OAUTH_URL or a SEPAY_*_API_URL
#     pointing at a dev backend. Leaving them set sends case 10 at a real server instead
#     of the local socket, which makes the case both flaky and impolite.
export HOME=$WORK/home
export XDG_CONFIG_HOME=$WORK/home/.config
export XDG_CACHE_HOME=$WORK/cache
while read -r v; do [ -n "$v" ] && unset "$v"; done < <(env | sed -n 's/^\(SEPAY_[A-Za-z0-9_]*\)=.*/\1/p')

trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
results=()

# `out` must be cleared before each case: if a case fails before reassigning it, the
# note printed is the PREVIOUS case's correct result and the table blames the wrong row.
reset() { out=""; }

record() { # record <id> <case name> <PASS|FAIL|SKIP> [note]
  results+=("$1|$2|$3|${4:-}")
  case $3 in
  PASS) pass=$((pass + 1)) ;;
  FAIL) fail=$((fail + 1)) ;;
  esac
}

[ -d "$OUT" ] || {
  echo "Error: dist/npm/ does not exist. Run: node npm/build-packages.js" >&2
  exit 1
}

VERSION=$(node -p "require('$OUT/sepay/package.json').version")
# The published names come from the build, not from literals repeated here, so this
# suite runs unchanged against a dev namespace (SEPAY_NPM_SCOPE / SEPAY_NPM_PKG).
PKG=$(node -p "require('$OUT/sepay/package.json').name")
PSCOPE=$(node -p "Object.keys(require('$OUT/sepay/package.json').optionalDependencies)[0].split('/')[0]")

# How many platform packages npm put in node_modules (run from the project dir).
count_platforms() {
  n=0
  for d in "node_modules/$PSCOPE"/cli-*/; do [ -d "$d" ] && n=$((n + 1)); done
  echo "$n"
}
# npm pack flattens a scope into the filename: @a/b -> a-b-<version>.tgz
WRAP_TGZ=$(printf '%s' "$PKG" | sed 's|^@||; s|/|-|g')-$VERSION.tgz
PLAT_TGZ=$(printf '%s' "$PSCOPE" | sed 's|^@||')-cli-linux-x64-$VERSION.tgz
for d in "$OUT"/*/; do (cd "$d" && npm pack --pack-destination "$TGZ" --silent >/dev/null); done

# A server that accepts the connection and never answers. It gives cases 10 and 10b a
# sepay process that blocks indefinitely without touching the network — `sepay login`
# opens the device flow against it and sits in the HTTP read.
cat >"$WORK/hang.js" <<'EOF'
const net = require("net");
const fs = require("fs");
const s = net.createServer(() => {
  try { fs.writeFileSync(process.argv[3], "1"); } catch (e) {}
});
s.listen(0, "127.0.0.1", () => fs.writeFileSync(process.argv[2], String(s.address().port)));
EOF

# Every test project pins the 6 platform packages at the local .tgz files, since they do
# not exist on a real registry yet.
write_project() {
  mkdir -p "$1" && cd "$1" || exit 1
  node -e "
const fs=require('fs');
const keys=['linux-x64','linux-arm64','darwin-x64','darwin-arm64','win32-x64','win32-arm64'];
const o={};
const scope='$PSCOPE', flat=scope.replace(/^@/,'');
for(const k of keys) o[scope+'/cli-'+k]='file:$TGZ/'+flat+'-cli-'+k+'-$VERSION.tgz';
fs.writeFileSync('package.json', JSON.stringify(
  {name:'t',version:'1.0.0',private:true,overrides:o,resolutions:o}, null, 2));
"
}

# An empty project with no overrides — for the cases that pull from a real registry.
write_plain_project() {
  mkdir -p "$1" && cd "$1" || exit 1
  node -e "require('fs').writeFileSync('package.json', JSON.stringify(
    {name:'t',version:'1.0.0',private:true},null,2))"
}

# case 3: a customer who installs with --ignore-scripts still gets a working CLI
write_project "$WORK/c3"
npm install --ignore-scripts "$TGZ/$WRAP_TGZ" --silent >/dev/null 2>&1
if out=$(./node_modules/.bin/sepay version 2>&1) && [[ $out == *"$VERSION"* ]]; then
  record 3 "install with --ignore-scripts" PASS "$out"
else
  record 3 "install with --ignore-scripts" FAIL "$out"
fi

# npm downloads only the matching package
# Count only the platform packages: under a dev namespace the wrapper shares the scope
# directory with them, so a plain count of the directory would be one too many.
n=$(count_platforms)
[ "$n" = "1" ] && record "3b" "only 1/6 platform packages installed" PASS "$(du -sh node_modules | cut -f1)" ||
  record "3b" "only 1/6 platform packages installed" FAIL "$n packages"

# case 14: the exec bit survives pack -> install
# Two distinct checks, do not merge them:
#   (a) INSIDE the .tgz it must be 0755 — that is what the release decides, and the only
#       thing that reaches a customer's machine.
#   (b) After install the exec bit must still be there and the binary must run. Do NOT
#       assert "755": the on-disk mode also depends on the machine's umask (this one
#       runs umask 002, giving 775). Asserting 755 turns a developer's local config into
#       a red build and hides the real failure, which is "lost the exec bit".
BIN=$PWD/node_modules/$PSCOPE/cli-linux-x64/bin/sepay
intgz=$(tar -tvzf "$TGZ/$PLAT_TGZ" | awk '/bin\/sepay$/{print $1}')
mode=$(stat -c '%a' "$BIN")
if [ "$intgz" = "-rwxr-xr-x" ] && [ -x "$BIN" ]; then
  record 14 "exec bit survives (--ignore-scripts)" PASS "in .tgz=$intgz, on disk=$mode (umask $(umask))"
else
  record 14 "exec bit survives (--ignore-scripts)" FAIL "in .tgz=$intgz, on disk=$mode"
fi

# case 11: an unknown command must exit non-zero
# `sepay docs exit-codes` documents 2 for "bad flags, bad arguments, unknown command",
# and that contract has to survive the shim.
./node_modules/.bin/sepay no-such-command >/dev/null 2>&1
code=$?
[ "$code" = "2" ] && record 11 "unknown command -> exit 2 through the shim" PASS "exit=$code" ||
  record 11 "unknown command -> exit 2 through the shim" FAIL "exit=$code (want 2)"

# case 9 (the part that runs on Linux): arguments arrive intact.
#
# sepay has no command that echoes its arguments, so this uses two properties the CLI
# does give us. `sepay <unknown>` quotes the argument back verbatim, which checks each
# metacharacter survives; and an over-supplied command reports how many arguments it
# received, which checks the shim did not split one argument into several. A shell in
# the spawn path would break both.
bad=""
for a in 'a & b' '|pipe|' '^caret^' '"quoted"' '$(echo pwned)' '*' 'a;b' '`id`'; do
  got=$(./node_modules/.bin/sepay "$a" 2>&1)
  # The CLI quotes the argument with Go's %q, which backslash-escapes a backslash and a
  # double quote. Re-apply that escaping to build the needle, so `"quoted"` proves the
  # argument arrived intact rather than failing on the reporting format.
  esc=${a//\\/\\\\}; esc=${esc//\"/\\\"}
  grep -qF -- "unknown command \"$esc\"" <<<"$got" || { bad=$a; break; }
done
count=$(./node_modules/.bin/sepay docs 'a & b' '|pipe|' '$(echo pwned)' 2>&1)
if [ -z "$bad" ] && grep -qF -- "received 3" <<<"$count"; then
  record "9-linux" "shell metacharacters in args arrive intact, arg count preserved" PASS "8 metacharacter args + a 3-arg count"
else
  record "9-linux" "shell metacharacters in args arrive intact, arg count preserved" FAIL "mangled=[${bad:-none}] count=$(tr '\n' ' ' <<<"$count")"
fi

# case 10 (the part that runs on Linux): Ctrl-C is forwarded to the binary.
#
# The assertion is NOT the CLI's shutdown message — that wording belongs to the Go
# runtime and changes with it. It is that the child is GONE and the shim exited
# promptly. A shim that exits on SIGINT without forwarding also exits promptly, but
# leaves the binary orphaned and still running, which is what this catches.
reset
node "$WORK/hang.js" "$WORK/port" "$WORK/connected" &
hang=$!
for _ in $(seq 1 50); do [ -s "$WORK/port" ] && break; sleep 0.1; done
PORT=$(cat "$WORK/port" 2>/dev/null || true)
if [ -z "$PORT" ]; then
  record "10-linux" "Ctrl-C reaches the binary, no orphan" FAIL "could not start the local stub server"
  record "10b" "binary killed with -9 -> exit 137" FAIL "could not start the local stub server"
else
  ./node_modules/.bin/sepay login --hostname "http://127.0.0.1:$PORT" </dev/null >"$WORK/c10.log" 2>&1 &
  shim=$!
  for _ in $(seq 1 100); do [ -s "$WORK/connected" ] && break; sleep 0.1; done
  child=$(pgrep -P "$shim" 2>/dev/null | head -1)
  kill -INT "$shim" 2>/dev/null
  wait "$shim"
  code=$?
  sleep 0.2
  if [ -n "$child" ] && [ "$code" != "0" ] && ! kill -0 "$child" 2>/dev/null; then
    record "10-linux" "Ctrl-C reaches the binary, no orphan" PASS "exit=$code, child $child gone"
  else
    record "10-linux" "Ctrl-C reaches the binary, no orphan" FAIL \
      "exit=$code child=${child:-none} $(tr '\n' ' ' <"$WORK/c10.log")"
  fi

  # case 10b: the binary is killed hard -> the shim must report 128+9, not 0 and not 1.
  # A customer's CI reading 0 here would treat a killed payment command as a success.
  reset
  rm -f "$WORK/connected"
  ./node_modules/.bin/sepay login --hostname "http://127.0.0.1:$PORT" </dev/null >/dev/null 2>&1 &
  shim=$!
  for _ in $(seq 1 100); do [ -s "$WORK/connected" ] && break; sleep 0.1; done
  pkill -KILL -P "$shim" 2>/dev/null
  wait "$shim"
  code=$?
  [ "$code" = "137" ] && record "10b" "binary killed with -9 -> exit 137" PASS ||
    record "10b" "binary killed with -9 -> exit 137" FAIL "exit=$code"
fi
kill "$hang" 2>/dev/null

# case 12: flip one byte inside the platform package
cd "$WORK/c3" || exit 1
printf 'X' | dd of="$BIN" bs=1 seek=100 conv=notrunc status=none
if out=$(./node_modules/.bin/sepay version 2>&1); then
  record 12 "one flipped byte -> must STOP" FAIL "still ran: $out"
else
  [[ $out == *"checksum verification failed"* ]] &&
    record 12 "one flipped byte -> must STOP" PASS "exit 1, with Expected/Actual" ||
    record 12 "one flipped byte -> must STOP" FAIL "$out"
fi

# case 2: --omit=optional
write_project "$WORK/c2"
npm install --ignore-scripts --omit=optional "$TGZ/$WRAP_TGZ" --silent >/dev/null 2>&1
if [ -x ./node_modules/.bin/sepay ]; then
  out=$(./node_modules/.bin/sepay version 2>&1)
  code=$?
  [ "$code" != "0" ] && [[ $out == *"is not installed"* ]] &&
    record 2 "--omit=optional -> a clear error" PASS "exit=$code" ||
    record 2 "--omit=optional -> a clear error" FAIL "exit=$code $out"
else
  record 2 "--omit=optional -> a clear error" PASS "npm drops the command from .bin/ entirely (known)"
fi

# case 8: monorepo (a workspace package, node_modules hoisted to the root)
write_project "$WORK/c8"
node -e "
const fs=require('fs'),p=require('./package.json');
p.workspaces=['packages/*']; fs.writeFileSync('package.json',JSON.stringify(p,null,2));
fs.mkdirSync('packages/app',{recursive:true});
fs.writeFileSync('packages/app/package.json', JSON.stringify(
  {name:'app',version:'1.0.0',dependencies:{'$PKG':'file:$TGZ/$WRAP_TGZ'}},null,2));
"
npm install --ignore-scripts --silent >/dev/null 2>&1
if out=$(./node_modules/.bin/sepay version 2>&1) && [[ $out == *"$VERSION"* ]]; then
  record 8 "monorepo (workspaces, hoisted packages)" PASS
else
  record 8 "monorepo (workspaces, hoisted packages)" FAIL "$out"
fi

# case 13: preferUnplugged is on EVERY platform package
missing=""
for d in "$OUT"/cli-*/; do
  node -e "process.exit(require('$d/package.json').preferUnplugged === true ? 0 : 1)" ||
    missing="$missing $(basename "$d")"
done
[ -z "$missing" ] && record 13 "preferUnplugged: true on all 6 packages" PASS ||
  record 13 "preferUnplugged: true on all 6 packages" FAIL "missing:$missing"

# cases 4-7: pnpm / Yarn 1 / modern Yarn / bun
# Only with a real registry to point at. Each tool handles optionalDependencies its own
# way and that is exactly what needs measuring.
have() { command -v "$1" >/dev/null 2>&1; }

# Count the entries in a directory whose name mentions sepay. A glob rather than
# `ls | grep`, so a name with a space or a newline cannot inflate the count.
count_cached() {
  local n=0 f
  for f in "$1"/*sepay*; do [ -e "$f" ] && n=$((n + 1)); done
  printf '%s' "$n"
}

if [ -z "$REGISTRY" ]; then
  record 4 "pnpm" SKIP "no registry — rerun with a registry address"
  record 5 "Yarn 1" SKIP "no registry"
  record 6 "Yarn modern" SKIP "no registry"
  record 7 "bun" SKIP "no registry"
else
  # case 4 — pnpm
  if have pnpm; then
    reset; write_plain_project "$WORK/c4"
    if pnpm add "$PKG@$VERSION" --registry "$REGISTRY" >/dev/null 2>&1 &&
      out=$(./node_modules/.bin/sepay version 2>&1) && [[ $out == *"$VERSION"* ]]; then
      record 4 "pnpm $(pnpm --version)" PASS "$(du -sh node_modules | cut -f1)"
    else
      record 4 "pnpm $(pnpm --version)" FAIL "${out:-install failed}"
    fi
  else
    record 4 "pnpm" SKIP "pnpm not installed"
  fi

  # case 5 — Yarn 1. Measure what it DOWNLOADS, not only what it installs: Yarn 1 puts
  # only the matching package in node_modules but still pulls all 6 into its cache.
  if have yarn && [[ $(yarn --version) == 1.* ]]; then
    reset; write_plain_project "$WORK/c5"
    cachedir=$(yarn cache dir 2>/dev/null)
    before=$(count_cached "$cachedir")
    if yarn add "$PKG@$VERSION" --registry "$REGISTRY" --silent >/dev/null 2>&1 &&
      out=$(./node_modules/.bin/sepay version 2>&1) && [[ $out == *"$VERSION"* ]]; then
      pulled=$(count_cached "$cachedir")
      record 5 "Yarn $(yarn --version) (classic)" PASS \
        "installed $(count_platforms)/6, but DOWNLOADED $((pulled - before))"
    else
      record 5 "Yarn $(yarn --version) (classic)" FAIL "${out:-install failed}"
    fi
  else
    record 5 "Yarn 1" SKIP "yarn 1 not installed"
  fi

  # case 6 — modern Yarn. preferUnplugged must unpack the binary to a real file.
  if have yarn; then
    reset; write_plain_project "$WORK/c6"
    yarn set version berry >/dev/null 2>&1
    yarn config set npmRegistryServer "$REGISTRY" >/dev/null 2>&1
    yarn config set unsafeHttpWhitelist localhost >/dev/null 2>&1
    yarn config set enableGlobalCache false >/dev/null 2>&1
    gate=$(yarn config npmMinimalAgeGate 2>/dev/null | awk '/Value:/{print $NF}')
    yarn config set npmMinimalAgeGate 0 >/dev/null 2>&1
    if yarn add "$PKG@$VERSION" >/dev/null 2>&1 &&
      out=$(yarn sepay version 2>&1 | tail -1) && [[ $out == *"$VERSION"* ]] &&
      unplugged=$(find .yarn/unplugged -type f -name sepay 2>/dev/null | head -1) &&
      [ -x "$unplugged" ]; then
      record 6 "Yarn $(yarn --version) (modern)" PASS "preferUnplugged gives a real file; default age gate ${gate}min"
    else
      record 6 "Yarn $(yarn --version) (modern)" FAIL "${out:-install failed}"
    fi
  else
    record 6 "Yarn modern" SKIP "yarn not installed"
  fi

  # case 7 — bun
  if have bun; then
    reset; write_plain_project "$WORK/c7"
    if bun add "$PKG@$VERSION" --registry "$REGISTRY" >/dev/null 2>&1 &&
      out=$(./node_modules/.bin/sepay version 2>&1) && [[ $out == *"$VERSION"* ]]; then
      n=$(count_platforms)
      # Running is one thing; pulling all 6 packages is another, and it belongs in the
      # table. That is the price a bun user pays, not a footnote.
      if [ "$n" = "1" ]; then
        record 7 "bun $(bun --version)" PASS "installed $n/6, $(du -sh node_modules | cut -f1)"
      else
        record 7 "bun $(bun --version)" FAIL "runs, but installed $n/6 ($(du -sh node_modules | cut -f1)) — no os/cpu filtering"
      fi
    else
      record 7 "bun $(bun --version)" FAIL "${out:-install failed}"
    fi
  else
    record 7 "bun" SKIP "bun not installed"
  fi
fi

# cases that cannot run here
record 1 "lockfile made on macOS, installed on Linux" SKIP "needs a macOS machine"
record 9 "Windows: special characters in arguments" SKIP "needs a Windows machine"
record 10 "Windows: Ctrl-C during a long command" SKIP "needs a Windows machine"

# results
cd "$ROOT" || exit 1
printf '\n%-9s %-62s %-7s %s\n' "CASE" "WHAT IT CHECKS" "RESULT" "NOTE"
printf '%.0s-' {1..126}
printf '\n'
for r in "${results[@]}"; do
  IFS='|' read -r id name verdict note <<<"$r"
  printf '%-9s %-62s %-7s %s\n' "$id" "$name" "$verdict" "$note"
done
printf '\nversion %s — %d passed, %d failed, %d skipped\n' \
  "$VERSION" "$pass" "$fail" "$((${#results[@]} - pass - fail))"
[ "$fail" -eq 0 ] || exit 1
