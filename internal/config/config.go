// Package config reads and writes the sepay config (config.yml).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Dir returns SEPAY_CONFIG_DIR, else the OS user config dir plus "sepay".
func Dir() (string, error) {
	if custom := os.Getenv("SEPAY_CONFIG_DIR"); custom != "" {
		return custom, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sepay"), nil
}

// Env values; sandbox is the default.
const (
	EnvSandbox    = "sandbox"
	EnvProduction = "production"
)

// ValidEnv reports whether env names a supported environment.
func ValidEnv(env string) bool { return env == EnvSandbox || env == EnvProduction }

// Config is the loaded sepay configuration: a YAML document whose dotted keys name nested
// sections ("sandbox.api_url"), matched case-insensitively.
type Config struct {
	values map[string]any
	dir    string
	// file is the --config path, used for both reads and writes.
	file string
}

// configNames are read in this order; config.yml comes first because Save writes it.
var configNames = []string{"config.yml", "config.yaml"}

// otherFormats are the config.<ext> files earlier releases picked up; finding one alone is an error
// rather than a silently ignored file.
var otherFormats = []string{".json", ".toml", ".properties", ".props", ".prop", ".hcl", ".tfvars", ".dotenv", ".env", ".ini"}

// Load reads config.yml from the standard config directory.
func Load() (*Config, error) { return load("") }

// LoadFrom reads config from an exact file path (--config).
func LoadFrom(file string) (*Config, error) { return load(file) }

func load(file string) (*Config, error) {
	dir, path, err := locate(file)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	// Environment variables are never read: SEPAY_DEFAULT_ENV would silently arm production.
	values := map[string]any{}
	if path != "" {
		data, err := os.ReadFile(path)
		if file != "" && errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no config file at %s; create it, or set SEPAY_CONFIG_DIR to a directory and sepay writes config.yml there itself", file)
		}
		if err != nil {
			return nil, err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		values = lowerKeys(doc)
	}

	c := &Config{values: values, dir: dir, file: file}
	if values["default_env"] == nil {
		c.Set("default_env", EnvSandbox)
	}
	if env := c.Get("default_env"); !ValidEnv(env) {
		slog.Warn("ignoring invalid default_env", "value", env, "dir", dir, "using", EnvSandbox)
		c.Set("default_env", EnvSandbox)
	}
	return c, nil
}

// locate returns the config directory and the file to read, "" when there is none yet.
func locate(file string) (dir, path string, err error) {
	if file != "" {
		// JSON is read too: it is a subset of YAML.
		switch ext := filepath.Ext(file); ext {
		case "", ".yml", ".yaml", ".json":
		default:
			return "", "", unsupportedFormat(ext)
		}
		return filepath.Dir(file), file, nil
	}

	dir, err = Dir()
	if err != nil {
		return "", "", err
	}
	for _, name := range configNames {
		if p := filepath.Join(dir, name); fileExists(p) {
			return dir, p, nil
		}
	}
	for _, ext := range otherFormats {
		p := filepath.Join(dir, "config"+ext)
		if !fileExists(p) {
			continue
		}
		if ext == ".json" {
			// --config still reads JSON; only the lookup in the config directory stopped.
			return "", "", fmt.Errorf("%s is not read; rename it to config.yml (JSON is valid YAML)", p)
		}
		return "", "", unsupportedFormat(ext)
	}
	return dir, "", nil
}

func unsupportedFormat(ext string) error {
	return fmt.Errorf("unsupported config format %s; use YAML", ext)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// lowerKeys copies a decoded YAML mapping with every key lowercased, at every depth.
func lowerKeys(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = lowerValue(v)
	}
	return out
}

func lowerValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		return lowerKeys(v)
	case map[any]any:
		m := make(map[string]any, len(v))
		for k, inner := range v {
			m[fmt.Sprint(k)] = inner
		}
		return lowerKeys(m)
	}
	return v
}

// Get returns the string value for key, "" when it is unset or names a section or list.
func (c *Config) Get(key string) string {
	var node any = c.values
	for _, part := range strings.Split(strings.ToLower(key), ".") {
		section, ok := node.(map[string]any)
		if !ok {
			return ""
		}
		node = section[part]
	}
	switch v := node.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int, int64, uint64, float64:
		return fmt.Sprint(v)
	}
	return ""
}

// Set stores value for key in memory, replacing any value in the way of its section; call Save
// to persist.
func (c *Config) Set(key, value string) {
	parts := strings.Split(strings.ToLower(key), ".")
	section := c.values
	for _, part := range parts[:len(parts)-1] {
		next, ok := section[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			section[part] = next
		}
		section = next
	}
	section[parts[len(parts)-1]] = value
}

// Dir returns the config directory in use.
func (c *Config) Dir() string { return c.dir }

// Save writes the whole document, default_env included, as YAML (JSON for a --config *.json) with
// mode 0600. It writes a temp file and renames it over the target, so an interrupted Save never
// leaves a half-written config.
func (c *Config) Save() error {
	dst := c.file
	if dst == "" {
		dst = filepath.Join(c.dir, configNames[0])
	}
	// A --config file read as JSON is written back as JSON, so the file keeps the format its name says.
	var data []byte
	var err error
	if filepath.Ext(dst) == ".json" {
		if data, err = json.MarshalIndent(c.values, "", "  "); err == nil {
			data = append(data, '\n')
		}
	} else {
		data, err = yaml.Marshal(c.values)
	}
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, "config-*.yml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
