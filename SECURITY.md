# Security Policy

## Report a vulnerability

Please report security issues privately to **support@sepay.vn**; do not open a public issue for
anything security-sensitive. Include steps to reproduce and the affected version (`sepay version`).
We aim to acknowledge within a few business days.

## How a vulnerable version is handled

1. The affected range is found (`git tag --contains <commit that introduced it>`).
2. Severe issues: new installs are moved to a safe version at once (`latest` dist-tag) while the fix
   is prepared.
3. The fix ships as a patch release through the normal release flow.
4. Every affected version of all 7 npm packages is marked with `npm deprecate` pointing at the fixed
   version, and the affected GitHub Releases get a warning at the top of their notes.
5. Details are published once users have had time to upgrade.

Only the latest release receives fixes before 1.0.

## No-secrets policy

`sepay` is open source and **ships no secret**. It must never embed an API key, token, signing key,
internal endpoint, or host credential.

- The CLI authenticates only with **OAuth2 tokens** the user obtains via `sepay login`; it never
  carries a static SePay API key.
- Tokens are stored in the **OS keychain**, or in a `0600` file only when the user chooses
  `--insecure-storage`; a keychain failure is an error, never a silent switch to the file.
  Tokens are **never printed** (`login`, `auth status`, `--verbose`, and errors redact them).
  Only `sepay auth token` prints one, explicitly.
- A session comes only from `sepay login`; there is no environment variable that injects a token.
- The CLI calls only the **public** SePay API, no CLI-private or internal endpoint. The one other
  host is `registry.npmjs.org`, asked at most once a day for the latest version; it receives no
  credential (`SEPAY_NO_UPDATE_NOTIFIER=1` turns it off).

If you find a hardcoded secret in the repository or a released binary, treat it as a vulnerability
and report it as above.

## Supply chain

- Every commit is scanned with gitleaks in CI; a test's fake credential is marked `// gitleaks:allow`
  on its own line.
- Release binaries are built by GitHub Actions only (`.github/workflows/release.yml`), with every
  action pinned by full commit SHA.
- `checksums.txt` is signed keyless with cosign (GitHub OIDC); the npm job refuses to package an
  archive whose checksum is not in the signed file.
- npm packages are published by the same workflow through npm trusted publishing; no npm token is
  stored in the repository.

## Supported versions

During pre-1.0 development, only the latest release is supported.
