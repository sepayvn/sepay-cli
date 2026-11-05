package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), []byte(body), 0o600))
	t.Setenv("SEPAY_CONFIG_DIR", dir)
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
	for _, env := range []string{"staging", "prod", "PRODUCTION", `""`} {
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

func TestLoad_DefaultEnvLeftEmptyIsWarnedAbout(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	writeConfig(t, "default_env:\n")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, EnvSandbox, cfg.Get("default_env"))
	assert.Empty(t, logs.String(), "a null default_env takes the default quietly")

	writeConfig(t, "default_env: \"\"\n")
	cfg, err = Load()
	require.NoError(t, err)
	assert.Equal(t, EnvSandbox, cfg.Get("default_env"))
	assert.Contains(t, logs.String(), "ignoring invalid default_env")
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

func loadDir(t *testing.T, files map[string]string) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	t.Setenv("SEPAY_CONFIG_DIR", dir)
	cfg, err := Load()
	require.NoError(t, err)
	return cfg, dir
}

func saved(t *testing.T, cfg *Config, dir string) string {
	t.Helper()
	require.NoError(t, cfg.Save())
	b, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	require.NoError(t, err)
	return string(b)
}

func TestConfig_DottedKeyIsNestedYAML(t *testing.T) {
	cfg, dir := loadDir(t, map[string]string{"config.yml": "sandbox:\n  api_url: http://a/v2\n"})
	assert.Equal(t, "http://a/v2", cfg.Get("sandbox.api_url"))
	assert.Empty(t, cfg.Get("sandbox"), "a section is not a value")
	assert.Empty(t, cfg.Get("sandbox.api_url.deeper"))

	cfg.Set("production.pg_api_url", "http://b")
	assert.Equal(t, "default_env: sandbox\nproduction:\n    pg_api_url: http://b\nsandbox:\n    api_url: http://a/v2\n", saved(t, cfg, dir))
}

func TestConfig_KeysAreCaseInsensitive(t *testing.T) {
	cfg, dir := loadDir(t, map[string]string{"config.yml": "Sandbox:\n  API_URL: http://a/v2\nDEFAULT_ENV: production\n"})
	assert.Equal(t, "http://a/v2", cfg.Get("sandbox.api_url"))
	assert.Equal(t, "http://a/v2", cfg.Get("SANDBOX.Api_Url"))
	assert.Equal(t, EnvProduction, cfg.Get("default_env"))

	cfg.Set("Sandbox.Api_Url", "http://c/v2")
	assert.Equal(t, "http://c/v2", cfg.Get("sandbox.api_url"))
	assert.Equal(t, "default_env: production\nsandbox:\n    api_url: http://c/v2\n", saved(t, cfg, dir))
}

func TestConfig_ScalarsReadAsStrings(t *testing.T) {
	cfg, _ := loadDir(t, map[string]string{"config.yml": "sandbox:\n  api_url: 8080\n  flag: true\n  list: [a, b]\n"})
	assert.Equal(t, "8080", cfg.Get("sandbox.api_url"))
	assert.Equal(t, "true", cfg.Get("sandbox.flag"))
	assert.Empty(t, cfg.Get("sandbox.list"))
}

func TestConfig_SaveWritesTheDefaultAndKeepsUnknownKeys(t *testing.T) {
	cfg, dir := loadDir(t, nil)
	assert.Equal(t, "default_env: sandbox\n", saved(t, cfg, dir))

	cfg, dir = loadDir(t, map[string]string{"config.yml": "future_key: kept\n"})
	cfg.Set("sandbox.api_url", "")
	assert.Equal(t, "default_env: sandbox\nfuture_key: kept\nsandbox:\n    api_url: \"\"\n", saved(t, cfg, dir))
}

func TestConfig_SaveIsPrivateAtomicAndRepeatable(t *testing.T) {
	cfg, dir := loadDir(t, map[string]string{"config.yml": "sandbox:\n  api_url: http://a/v2\n"})
	first := saved(t, cfg, dir)
	assert.Equal(t, first, saved(t, cfg, dir), "a second Save changes nothing")

	info, err := os.Stat(filepath.Join(dir, "config.yml"))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp file is left behind")
}

func TestConfig_FindsConfigYAMLInTheDirectory(t *testing.T) {
	cfg, _ := loadDir(t, map[string]string{"config.yaml": "default_env: production\n"})
	assert.Equal(t, EnvProduction, cfg.Get("default_env"))
}

func TestConfig_PrefersTheFileSaveWrites(t *testing.T) {
	cfg, _ := loadDir(t, map[string]string{
		"config.yml":  "default_env: production\n",
		"config.yaml": "default_env: sandbox\n",
	})
	assert.Equal(t, EnvProduction, cfg.Get("default_env"), "config set writes config.yml, so it must be the file read back")
}

func TestConfig_RefusesNonYAMLFormats(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("default_env = \"production\"\n"), 0o600))
	t.Setenv("SEPAY_CONFIG_DIR", dir)
	_, err := Load()
	require.EqualError(t, err, "unsupported config format .toml; use YAML")

	named := filepath.Join(t.TempDir(), "my.toml")
	require.NoError(t, os.WriteFile(named, []byte("default_env = \"production\"\n"), 0o600))
	_, err = LoadFrom(named)
	require.EqualError(t, err, "unsupported config format .toml; use YAML")
}

func TestConfig_JSONInTheConfigDirSaysHowToKeepIt(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"default_env":"production"}`), 0o600))
	t.Setenv("SEPAY_CONFIG_DIR", dir)
	_, err := Load()
	require.EqualError(t, err, filepath.Join(dir, "config.json")+" is not read; rename it to config.yml (JSON is valid YAML)")
}

func TestConfig_NamedJSONFileReadsAsYAML(t *testing.T) {
	named := filepath.Join(t.TempDir(), "my.json")
	require.NoError(t, os.WriteFile(named, []byte(`{"default_env": "production", "sandbox": {"api_url": "http://a/v2"}}`), 0o600))
	cfg, err := LoadFrom(named)
	require.NoError(t, err)
	assert.Equal(t, EnvProduction, cfg.Get("default_env"))
	assert.Equal(t, "http://a/v2", cfg.Get("sandbox.api_url"))
}

func TestConfig_MalformedYAMLIsAnError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), []byte("sandbox: [unclosed\n"), 0o600))
	t.Setenv("SEPAY_CONFIG_DIR", dir)
	_, err := Load()
	require.Error(t, err)
}

// A config.yml as v0.1.2 wrote it, local test hosts included.
func TestConfig_ReadsAFileV012Wrote(t *testing.T) {
	cfg, dir := loadDir(t, map[string]string{"config.yml": "default_env: sandbox\nsandbox:\n    api_url: http://127.0.0.1:18080/api/v2\n    bankhub_api_url: http://127.0.0.1:18080\n    einvoice_api_url: http://127.0.0.1:18080\n    pg_api_url: http://127.0.0.1:18080\n"})
	assert.Equal(t, "http://127.0.0.1:18080/api/v2", cfg.Get("sandbox.api_url"))
	assert.Equal(t, "http://127.0.0.1:18080", cfg.Get("sandbox.pg_api_url"))
	before, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	require.NoError(t, err)
	assert.Equal(t, string(before), saved(t, cfg, dir), "an unchanged file saves byte for byte")
}

func TestConfig_NamedJSONFileIsSavedAsJSON(t *testing.T) {
	named := filepath.Join(t.TempDir(), "my.json")
	require.NoError(t, os.WriteFile(named, []byte(`{"default_env": "sandbox"}`), 0o600))
	cfg, err := LoadFrom(named)
	require.NoError(t, err)
	cfg.Set("sandbox.api_url", "http://a/v2")
	require.NoError(t, cfg.Save())

	data, err := os.ReadFile(named)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc), "the file stays JSON: %s", data)
	assert.Equal(t, map[string]any{"default_env": "sandbox", "sandbox": map[string]any{"api_url": "http://a/v2"}}, doc)
}
