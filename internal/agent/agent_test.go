package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"none", nil, ""},
		{"claude code", map[string]string{"CLAUDECODE": "1"}, "claude-code"},
		{"gemini", map[string]string{"GEMINI_CLI": "1"}, "gemini-cli"},
		{"codex sandbox", map[string]string{"CODEX_SANDBOX": "seatbelt"}, "codex"},
		{"codex ci", map[string]string{"CODEX_CI": "1"}, "codex"},
		{"cursor", map[string]string{"CURSOR_AGENT": "1"}, "cursor"},
		{"copilot", map[string]string{"COPILOT_AGENT": "true"}, "copilot"},
		{"cline", map[string]string{"CLINE_ACTIVE": "true"}, "cline"},
		{"generic AI_AGENT", map[string]string{"AI_AGENT": "amp"}, "amp"},
		{"generic AGENT", map[string]string{"AGENT": "goose"}, "goose"},
		{"AI_AGENT beats a specific variable", map[string]string{"AI_AGENT": "amp", "CLAUDECODE": "1"}, "amp"},
		{"claude code as it really runs", map[string]string{"AI_AGENT": "claude-code_2-1-321_agent", "CLAUDECODE": "1"}, "claude-code_2-1-321_agent"},
		{"specific beats AGENT", map[string]string{"AGENT": "goose", "CURSOR_AGENT": "1"}, "cursor"},
		{"order among specifics", map[string]string{"CLINE_ACTIVE": "1", "CLAUDECODE": "1"}, "claude-code"},
		{"off value is unset", map[string]string{"CLAUDECODE": "0"}, ""},
		{"off values fall through", map[string]string{"AI_AGENT": "false", "CLAUDECODE": "no", "GEMINI_CLI": "1"}, "gemini-cli"},
		{"off is case-insensitive", map[string]string{"CLAUDECODE": " OFF "}, ""},
		{"sanitized", map[string]string{"AI_AGENT": "My Bot!"}, "mybot"},
		{"keeps dot underscore dash", map[string]string{"AI_AGENT": "Agent_v1.2-beta"}, "agent_v1.2-beta"},
		{"header injection stripped", map[string]string{"AI_AGENT": "x\r\nX-Evil: 1"}, "xx-evil1"},
		{"empty after sanitize falls through", map[string]string{"AI_AGENT": "!!!", "CLAUDECODE": "1"}, "claude-code"},
		{"non-ascii dropped", map[string]string{"AGENT": "trợ-lý"}, "tr-l"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Detect(env(tt.env)))
		})
	}
}

func TestDetectCapsLength(t *testing.T) {
	got := Detect(env(map[string]string{"AI_AGENT": strings.Repeat("a", 100)}))
	assert.Equal(t, strings.Repeat("a", maxNameLen), got)
}
