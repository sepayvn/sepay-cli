package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), []byte(body), 0o600))
	t.Setenv("SEPAY_CONFIG_DIR", dir)
	return dir
}

func TestLoad_EnvVarsDoNotBecomeConfigValues(t *testing.T) {
	writeConfig(t, "default_env: sandbox\n")
	t.Setenv("SEPAY_DEFAULT_ENV", "production")
	t.Setenv("SEPAY_TOKEN", "supersecrettoken")
	t.Setenv("SEPAY_SANDBOX.API_URL", "http://evil.example/v2")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, EnvSandbox, cfg.Get("default_env"), "SEPAY_DEFAULT_ENV must not arm production")
	assert.Empty(t, cfg.Get("token"))
	assert.Empty(t, cfg.Get("sandbox.api_url"))
}

func TestLoad_InvalidDefaultEnvFallsBackToSandbox(t *testing.T) {
	for _, env := range []string{"staging", "prod", "PRODUCTION"} {
		t.Run(env, func(t *testing.T) {
			writeConfig(t, "default_env: "+env+"\n")

			cfg, err := Load()

			require.NoError(t, err)
			assert.Equal(t, EnvSandbox, cfg.Get("default_env"))
		})
	}
}

func TestLoad_DefaultsToSandboxAndAcceptsProduction(t *testing.T) {
	writeConfig(t, "sandbox:\n  api_url: http://localhost:8079/api/v2\n")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, EnvSandbox, cfg.Get("default_env"))
	assert.Equal(t, "http://localhost:8079/api/v2", cfg.Get("sandbox.api_url"))

	writeConfig(t, "default_env: production\n")
	cfg, err = Load()
	require.NoError(t, err)
	assert.Equal(t, EnvProduction, cfg.Get("default_env"))
}

func TestValidEnv(t *testing.T) {
	assert.True(t, ValidEnv(EnvSandbox))
	assert.True(t, ValidEnv(EnvProduction))
	assert.False(t, ValidEnv(""))
	assert.False(t, ValidEnv("Production"))
}

func TestConfig_SaveWritesTheFileConfigNamed(t *testing.T) {
	dir := t.TempDir()
	named := filepath.Join(dir, "probe.yml")
	require.NoError(t, os.WriteFile(named, []byte("default_env: sandbox\n"), 0o600))

	cfg, err := LoadFrom(named)
	require.NoError(t, err)
	cfg.Set("default_env", EnvProduction)
	require.NoError(t, cfg.Save())

	back, err := LoadFrom(named)
	require.NoError(t, err)
	assert.Equal(t, EnvProduction, back.Get("default_env"))
	assert.NoFileExists(t, filepath.Join(dir, "config.yml"))
}

func TestConfig_MissingNamedFileSaysHowToCreateOne(t *testing.T) {
	_, err := LoadFrom(filepath.Join(t.TempDir(), "absent.yml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no config file at")
	assert.Contains(t, err.Error(), "SEPAY_CONFIG_DIR")
}
