#!/usr/bin/env bash
# One-time: create the 7 npm packages and bind each to this repository's release.yml
# through npm trusted publishing. Run on a maintainer machine after `npm login`
# (npm >= 11.15, account 2FA on). `npm trust` prints an npmjs.com/auth/cli link per
# package: confirm 2FA there within 5 minutes (E404 = link expired, rerun; E409 =
# already bound). Right after one confirmation npm may skip the next prompts.
set -euo pipefail

NPM=${NPM:-"npx -y npm@11.21.1"}
REPO=sepayvn/sepay-cli
WORKFLOW=release.yml
SCOPE=@sepayjsc
NAMES="sepay-cli"
for p in darwin-arm64 darwin-x64 linux-arm64 linux-x64 win32-arm64 win32-x64; do
  NAMES="$NAMES $SCOPE/cli-$p"
done

$NPM whoami >/dev/null || { echo "Not logged in: run '$NPM login' first" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for name in $NAMES; do
  if [ "$($NPM view "$name" name 2>/dev/null)" = "$name" ]; then
    echo "exists: $name"
  else
    dir="$work/${name//\//_}"
    mkdir -p "$dir"
    cat >"$dir/package.json" <<JSON
{
  "name": "$name",
  "version": "0.0.0-bootstrap",
  "description": "Placeholder that reserves the name for the sepay CLI. Install sepay-cli.",
  "license": "Apache-2.0",
  "repository": { "type": "git", "url": "git+https://github.com/$REPO.git" }
}
JSON
    printf '# %s\n\nPublished by https://github.com/%s releases. Install sepay-cli.\n' "$name" "$REPO" >"$dir/README.md"
    (cd "$dir" && $NPM stage publish --access public --tag bootstrap)
    echo "staged: $name"
  fi
done

for name in $NAMES; do
  $NPM trust github "$name" --repo "$REPO" --file "$WORKFLOW" --allow-publish --yes ||
    echo "check $name: E409 means it is already bound, anything else needs a rerun"
done

echo
echo "Done. Check: $NPM trust list sepay-cli"
