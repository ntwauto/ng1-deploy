package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// defaultConfigName is the filename the tool looks for in the current
// working directory if no other location is specified.
const defaultConfigName = "config.yaml"

// envConfigPath is the environment variable that, if set, points directly
// at the config file. This lets you relocate the config without editing
// source code or rebuilding, and without the circular problem of storing
// the config's own path inside the config file itself.
const envConfigPath = "NG1_CONFIG_PATH"

// pointerFileName is an optional tiny file that can sit next to the binary
// (or in the current directory) containing nothing but a single line: the
// path to the real config.yaml. This is useful if you want to relocate the
// config without setting an env var or CLI flag each time. It is NOT the
// config file itself, so there's no circular dependency.
const pointerFileName = "config.location"

// configSearchPaths lists fixed fallback locations checked, in order, if
// nothing else resolves. Edit this list (and rebuild) to change built-in
// fallback locations.
var configSearchPaths = []string{
	"/etc/ng1-deploy/config.yaml",
	"/opt/ng1-deploy/config.yaml",
}

type SSHConfig struct {
	Username              string `yaml:"username"` // usually left blank, prompted at runtime
	Password              string `yaml:"password"` // usually left blank, prompted at runtime
	Port                  int    `yaml:"port"`
	ConnectTimeoutSeconds int    `yaml:"connect_timeout_seconds"`
	CommandTimeoutSeconds int    `yaml:"command_timeout_seconds"`
}

type LoggingConfig struct {
	LogDir string `yaml:"log_dir"`
}

type Config struct {
	Port        string        `yaml:"port"`
	PamLineTmpl string        `yaml:"pam_line"`
	SSH         SSHConfig     `yaml:"ssh"`
	Logging     LoggingConfig `yaml:"logging"`
	Devices     []string      `yaml:"devices"`
	UsersAdd    []string      `yaml:"users_add"`
	UsersDelete []string      `yaml:"users_delete"`

	// loadedFrom records which path was actually used, for logging/diagnostics.
	loadedFrom string
}

// PAMLine renders the pam_line template with port substituted.
func (c *Config) PAMLine() string {
	return strings.ReplaceAll(c.PamLineTmpl, "{port}", c.Port)
}

// resolveConfigPath figures out where config.yaml actually lives, checking
// in this order:
//
//  1. explicitPath (from --config flag), if non-empty
//  2. NG1_CONFIG_PATH environment variable
//  3. ./config.yaml in the current working directory
//  4. A pointer file (./config.location or next to the binary) containing
//     a path to the real config.yaml
//  5. Fixed fallback locations in configSearchPaths
func resolveConfigPath(explicitPath string) (string, error) {
	// 1. Explicit --config flag, highest priority.
	if explicitPath != "" {
		abs, err := filepath.Abs(explicitPath)
		if err != nil {
			return "", fmt.Errorf("resolving --config %q: %w", explicitPath, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("--config points to %q but it doesn't exist: %w", abs, err)
		}
		return abs, nil
	}

	// 2. Environment variable override.
	if p := os.Getenv(envConfigPath); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("resolving %s=%q: %w", envConfigPath, p, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("%s points to %q but it doesn't exist: %w", envConfigPath, abs, err)
		}
		return abs, nil
	}

	// 3. Current directory default.
	if abs, err := filepath.Abs(defaultConfigName); err == nil {
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}

	// 4. Pointer file: current directory, then next to the executable.
	pointerCandidates := []string{}
	if abs, err := filepath.Abs(pointerFileName); err == nil {
		pointerCandidates = append(pointerCandidates, abs)
	}
	if exe, err := os.Executable(); err == nil {
		pointerCandidates = append(pointerCandidates, filepath.Join(filepath.Dir(exe), pointerFileName))
	}

	for _, pointerPath := range pointerCandidates {
		data, err := os.ReadFile(pointerPath)
		if err != nil {
			continue // pointer file doesn't exist here, keep looking
		}

		target := strings.TrimSpace(string(data))
		if target == "" {
			continue
		}

		abs, err := filepath.Abs(target)
		if err != nil {
			continue
		}

		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}

		return "", fmt.Errorf("pointer file %q specifies %q but that file doesn't exist", pointerPath, abs)
	}

	// 5. Fixed fallback locations.
	for _, candidate := range configSearchPaths {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}

	tried := append([]string{
		"--config flag (not provided)",
		fmt.Sprintf("$%s env var", envConfigPath),
		"./" + defaultConfigName,
	}, pointerCandidates...)
	tried = append(tried, configSearchPaths...)

	return "", fmt.Errorf("config file not found; looked in: %s", strings.Join(tried, ", "))
}

// LoadConfig resolves the config file location (see resolveConfigPath) and
// loads it. explicitPath comes from the --config CLI flag; pass "" if not set.
func LoadConfig(explicitPath string) (*Config, error) {
	path, err := resolveConfigPath(explicitPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing yaml config %q: %w", path, err)
	}

	cfg.loadedFrom = path

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	cfg.applyDefaults()

	return &cfg, nil
}

func (c *Config) validate() error {
	if c.Port == "" {
		return fmt.Errorf("port is required")
	}
	if c.PamLineTmpl == "" {
		return fmt.Errorf("pam_line is required")
	}
	if len(c.Devices) == 0 {
		return fmt.Errorf("devices list is empty; nothing to configure")
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.SSH.Port == 0 {
		c.SSH.Port = 22
	}
	if c.SSH.ConnectTimeoutSeconds == 0 {
		c.SSH.ConnectTimeoutSeconds = 15
	}
	if c.SSH.CommandTimeoutSeconds == 0 {
		c.SSH.CommandTimeoutSeconds = 60
	}
	if c.Logging.LogDir == "" {
		c.Logging.LogDir = "./ng1_logs"
	}
}
