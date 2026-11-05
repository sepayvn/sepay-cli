# Releasing sepay-cli

Maintainer notes. Contributors do not need any of this; see [CONTRIBUTING.md](../CONTRIBUTING.md).

Everything happens on GitHub; nobody builds or publishes from a laptop.

1. Every merge to `main` updates one open PR titled **`chore(main): release X.Y.Z`**
   (version bump in `.release-please-manifest.json` + the new `CHANGELOG.md` section).
2. To ship: review that PR and **merge it**. The `Release` workflow then
   - tags `vX.Y.Z` and creates the GitHub Release with the changelog as notes,
   - builds 6 binaries (linux / darwin / windows × amd64 / arm64) with GoReleaser, signs
     `checksums.txt` with cosign and attaches everything to the Release,
   - builds the 7 npm packages from the signed archives, checks them, publishes the 6 platform
     packages, re-downloads and compares checksums, test-installs, then publishes
     `sepay-cli` under the **`latest`** dist-tag.
3. Prerelease (to try a build before releasing it). Take X.Y.Z from the open release PR title:
   ```bash
   git tag -a v0.3.0-rc.1 -m v0.3.0-rc.1 origin/main && git push origin v0.3.0-rc.1
   ```
   | Tag | npm dist-tag | Meaning |
   |---|---|---|
   | `vX.Y.Z-alpha.N` | `alpha` | early, may change a lot |
   | `vX.Y.Z-beta.N` | `beta` | feature complete, still testing |
   | `vX.Y.Z-rc.N` | `next` | release candidate: this exact code becomes X.Y.Z |

   → a GitHub prerelease + that npm dist-tag (`npx sepay-cli@next version`). `latest` does not move. Push one tag at a time, never `git push --tags`.
   The tag must point at a commit of `main`: the workflow refuses any other commit before
   building anything (it protects against tagging the wrong commit; someone with write access
   could still edit `release.yml` in that commit, so keep write access to 1–2 people).
4. A release from a public repository publishes to npm with provenance and attests the release
   files; the repository variable `NPM_PROVENANCE=true` must be set, or the npm job refuses to
   publish. From a private repository npm refuses provenance and GitHub refuses the attestation, so
   `NPM_PROVENANCE` stays unset, the version is published without either, and it never gains them.
   To give a release provenance, the repository stays public until both the `goreleaser` and `npm`
   jobs finish. The `release` environment requires a reviewer's approval.
5. Homebrew (`sepayvn/homebrew-sepay-cli`) and Scoop (`sepayvn/scoop-sepay-cli`) get a new cask / manifest
   from the same GoReleaser run on every final release from a public repository, never on a
   prerelease: a private repository's downloads would 404 from the cask and manifest. The push uses a
   GitHub App token minted in `release.yml` (variable `RELEASER_APP_CLIENT_ID`, secret
   `RELEASER_APP_PRIVATE_KEY`; the App is installed on those two repositories only).
6. A broken version is never unpublished: fix forward with a new version, and mark the bad one
   `npm deprecate sepay-cli@X.Y.Z "use X.Y.Z+1"`.

## First-time setup (once per package)

npm only lets a trusted publisher be attached to a package that exists. A new package is created
with staged publishing, from a maintainer's machine (npm >= 11.15, account with 2FA):

```bash
npx -y npm@11.21.1 login
bash scripts/npm-bootstrap.sh     # stage the 7 names, then bind each to release.yml
```

`npm stage publish` of a name that does not exist creates it with a public placeholder version
`0.0.0-stage`; the staged content stays private and is never approved. `npm trust github` binds the
package to `sepayvn/sepay-cli` + `release.yml` with direct publish allowed. No npm token is stored
in GitHub. A new platform package (a new OS / CPU) needs the same two steps before its first release.

Useful checks:

```bash
npm view sepay-cli dist-tags
gh release list --repo sepayvn/sepay-cli
```
