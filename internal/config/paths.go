package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultConfigPath returns the resolved config file path using a fallback
// chain:
//
//  1. $CRMKIT_CONFIG environment variable (if set and non-empty)
//  2. $XDG_CONFIG_HOME/crmkit/config.yaml (if XDG_CONFIG_HOME is set)
//  3. ~/.config/crmkit/config.yaml
func DefaultConfigPath() string {
	if envPath := strings.TrimSpace(os.Getenv("CRMKIT_CONFIG")); envPath != "" {
		return envPath
	}

	return filepath.Join(xdgConfigHome(), "crmkit", "config.yaml")
}

// DefaultDBPath returns the resolved database path using a fallback chain:
//
//  1. $XDG_DATA_HOME/crmkit/crmkit.db (if XDG_DATA_HOME is set)
//  2. ~/.local/share/crmkit/crmkit.db
func DefaultDBPath() string {
	return filepath.Join(xdgDataHome(), "crmkit", "crmkit.db")
}

// EnsureDir creates all parent directories for the given file path if they do
// not already exist.
func EnsureDir(filePath string) error {
	dir := filepath.Dir(filePath)
	return os.MkdirAll(dir, 0o700)
}

func xdgConfigHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".config")
}

func xdgDataHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".local", "share")
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}

	// fallback for unusual environments
	return "/tmp/crmkit-" + strconv.Itoa(os.Getuid())
}
