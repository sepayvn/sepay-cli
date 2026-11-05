package cmdutil

// ExitCode is the process exit code, per command-reference.md (Exit codes).
type ExitCode int

// Exit codes returned by the CLI.
const (
	ExitOK             ExitCode = 0   // success
	ExitError          ExitCode = 1   // generic error
	ExitUsage          ExitCode = 2   // usage / bad flags
	ExitNotImplemented ExitCode = 3   // command exists but has no backend yet
	ExitAuth           ExitCode = 4   // not authenticated (run sepay login)
	ExitScope          ExitCode = 5   // token lacks the scope; the caller can widen it
	ExitForward        ExitCode = 6   // local handler forward failed
	ExitOTP            ExitCode = 7   // OTP required / invalid
	ExitBlocked        ExitCode = 8   // blocked by account config or in-app permission
	ExitInterrupted    ExitCode = 130 // Ctrl+C or Esc at a prompt, or SIGINT/SIGTERM
)

// errorCodes name each exit code for machine output.
var errorCodes = map[ExitCode]string{
	ExitError:          "error",
	ExitUsage:          "usage_error",
	ExitNotImplemented: "not_implemented",
	ExitAuth:           "unauthenticated",
	ExitScope:          "insufficient_scope",
	ExitForward:        "forward_failed",
	ExitOTP:            "otp_required",
	ExitBlocked:        "blocked",
	ExitInterrupted:    "interrupted",
}
