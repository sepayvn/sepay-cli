<!-- PR title = the squash commit message, read by release-please:
     feat: …  → minor (pre-1.0) · fix: / perf: … → patch · feat!: … or "BREAKING CHANGE:" → breaking (minor before 1.0, major after)
     docs: / test: / ci: / chore: / refactor: … → no release -->

## What and why

## How it was tested

## Checklist

- [ ] Every commit is signed off (`git commit -s`, see CONTRIBUTING.md)
- [ ] `make check` passes locally (gofumpt, go vet, golangci-lint, `go test ./... -race`, tidy check)
- [ ] Tests added or updated for the change
- [ ] `--json` output schema and exit codes are unchanged, or the title carries `!` (breaking)
- [ ] Help topics (`pkg/cmd/root/topics/`) and the README updated if behavior changed
- [ ] No secret, token or internal endpoint added; the CLI calls only the public SePay API
