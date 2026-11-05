// Package listen streams one SePay delivery channel over SSE and replays each delivery
// to a local HTTP handler.
//
//   - listen.go: flags, session mint and revoke, Run and the read loop, delivery rendering
//   - headers.go: which delivery headers are replayed, and credential withholding
//   - redact.go: card fields masked before a payload reaches stdout
//   - retry.go: the --forward-retries schedule and its queue
//   - claim.go: the per-delivery claim file that lets sessions on one handler send each retry once
//   - peers.go: following and mirroring the retry chains of other sessions
//   - control.go: the keyboard controls on a terminal (control_unix.go, control_windows.go)
package listen
