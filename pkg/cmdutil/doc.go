// Package cmdutil provides the shared Factory and error types used by every command.
//
//   - factory.go: Factory, New, env and base URL resolution, production and mutation confirmations
//   - factory_token.go: token resolution, refresh and renewal of a rejected token
//   - errors.go, exitcode.go: error types, exit codes and HandleError
//   - allowlist.go, origin.go, sandbox_guard.go: credential hosts and the sandbox-only guard
//   - args.go, flagcheck.go, flagalias.go, annotations.go, query.go, segment.go: argument and flag
//     validation, and hidden spellings of renamed flags
//   - prompt.go: interactive pickers and yes/no prompts
//   - render.go, complete.go, browser.go, stub.go: output helpers, completion, browser, stub commands
package cmdutil
