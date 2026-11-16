// Package agent detects the AI coding agent that launched the CLI, so the server can log which
// writes an agent ran. The result is self-declared by the environment: it labels log rows only and
// must never gate or authorize anything.
package agent

import "strings"

// maxNameLen bounds a name taken from a free-form variable so it stays one short User-Agent token.
const maxNameLen = 32

// probes are checked in order; the first one that is set wins. A probe with a fixed name reports
// that name; a probe without one reports the variable's own value, sanitized.
var probes = []struct {
	env  string
	name string
}{
	{env: "AI_AGENT"},
	{env: "CLAUDECODE", name: "claude-code"},
	{env: "GEMINI_CLI", name: "gemini-cli"},
	{env: "CODEX_SANDBOX", name: "codex"},
	{env: "CODEX_CI", name: "codex"},
	{env: "CURSOR_AGENT", name: "cursor"},
	{env: "COPILOT_AGENT", name: "copilot"},
	{env: "CLINE_ACTIVE", name: "cline"},
	{env: "AGENT"},
}

// offValues mirrors envvar.Enabled: an empty, 0, false, no or off value means not set.
var offValues = map[string]bool{"": true, "0": true, "false": true, "no": true, "off": true}

// Detect returns the agent name, or "" when no agent variable is set.
func Detect(getenv func(string) string) string {
	for _, p := range probes {
		raw := strings.TrimSpace(getenv(p.env))
		if offValues[strings.ToLower(raw)] {
			continue
		}
		if p.name != "" {
			return p.name
		}
		if name := sanitize(raw); name != "" {
			return name
		}
	}
	return ""
}

// sanitize lowercases and keeps only [a-z0-9._-], so the name cannot break the header or the log.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if b.Len() == maxNameLen {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
