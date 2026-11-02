// Package config reads/writes the sepay config (config.yml) via Viper.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
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

// Config is the loaded sepay configuration.
type Config struct {
	v   *viper.Viper
	dir string
	// file is the --config path, used for both reads and writes.
	file string
}

// Load reads config.yml from the standard config directory.
func Load() (*Config, error) { return load("") }

// LoadFrom reads config from an exact file path (--config).
func LoadFrom(file string) (*Config, error) { return load(file) }

func load(file string) (*Config, error) {
	var dir string
	v := viper.New()

	if file != "" {
		dir = filepath.Dir(file)
		v.SetConfigFile(file)
	} else {
		var err error
		dir, err = Dir()
		if err != nil {
			return nil, err
		}
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(dir)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	v.SetDefault("default_env", EnvSandbox)

	// No AutomaticEnv: SEPAY_DEFAULT_ENV would silently arm production.
	// A missing --config file is a bare *fs.PathError and stays an error.
	var notFound viper.ConfigFileNotFoundError
	if err := v.ReadInConfig(); err != nil && !errors.As(err, &notFound) {
		if file != "" && errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no config file at %s; create it, or set SEPAY_CONFIG_DIR to a directory and sepay writes config.yml there itself", file)
		}
		return nil, err
	}

	if env := v.GetString("default_env"); !ValidEnv(env) {
		slog.Warn("ignoring invalid default_env", "value", env, "dir", dir, "using", EnvSandbox)
		v.Set("default_env", EnvSandbox)
	}

	return &Config{v: v, dir: dir, file: file}, nil
}

// Get returns the string value for key.
func (c *Config) Get(key string) string { return c.v.GetString(key) }

// Set stores value for key in memory; call Save to persist.
func (c *Config) Set(key, value string) { c.v.Set(key, value) }

// Dir returns the config directory in use.
func (c *Config) Dir() string { return c.dir }

// Save writes config.yml as 0600; env-sourced values are not persisted.
func (c *Config) Save() error {
	dst := c.file
	if dst == "" {
		dst = filepath.Join(c.dir, "config.yml")
	}
	// Viper picks the write format from the file name, so the temp keeps the target's extension.
	ext := filepath.Ext(dst)
	if ext == "" {
		ext = ".yml"
	}
	tmp, err := os.CreateTemp(c.dir, "config-*"+ext)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpName) }()

	if err := c.v.WriteConfigAs(tmpName); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
