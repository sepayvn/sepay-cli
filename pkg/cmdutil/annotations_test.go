package cmdutil_test

import (
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
)

func TestConfirmsValue(t *testing.T) {
	tests := []struct {
		raw, env, want string
	}{
		{"always", config.EnvSandbox, "always"},
		{"always", config.EnvProduction, "always"},
		{"conditional", config.EnvProduction, "conditional"},
		{" always ", config.EnvSandbox, "always"},
		{"", config.EnvSandbox, ""},
		{"sandbox=always,production=terminal", config.EnvSandbox, "always"},
		{"sandbox=always,production=terminal", config.EnvProduction, "terminal"},
		{"sandbox=conditional,production=terminal", config.EnvSandbox, "conditional"},
		{"production=terminal", config.EnvProduction, "terminal"},
		{"production=terminal", config.EnvSandbox, ""},
		{" sandbox = always , production = terminal ", config.EnvProduction, "terminal"},
		{" sandbox = always , production = terminal ", config.EnvSandbox, "always"},
	}
	for _, tt := range tests {
		t.Run(tt.raw+"@"+tt.env, func(t *testing.T) {
			assert.Equal(t, tt.want, cmdutil.ConfirmsValue(tt.raw, tt.env))
		})
	}
}
