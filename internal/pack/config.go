// Package pack copies each Catalyst session off an SD card into its own
// directory: the CSV, the raw video, and the numbers that line the two up.
package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/exotherm/exotherm-go/internal/export"
	"gopkg.in/yaml.v3"
)

// Config is the user file at ~/.config/exotherm/config.yaml. Empty fields keep
// the defaults: dest ~/exotherm, tmp ~/exotherm/tmp, speed mph, cleanup on.
type Config struct {
	Dest    string `yaml:"dest"`
	Tmp     string `yaml:"tmp"`
	Speed   string `yaml:"speed"`
	Cleanup *bool  `yaml:"cleanup"`
}

// ConfigPath is where pack looks for the user file.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "exotherm", "config.yaml"), nil
}

// LoadConfig reads the user file. A missing file is the defaults.
func LoadConfig() (Config, error) {
	cfg := Config{Speed: "mph"}
	path, err := ConfigPath()
	if err != nil {
		return cfg, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	if cfg.Speed == "" {
		cfg.Speed = "mph"
	}
	return cfg, nil
}

// Resolve fills dest and tmp. tmp defaults to dest/tmp. Paths starting with
// ~/ are expanded. cleanup is true unless the file sets it false.
func (c Config) Resolve(home string) (dest, tmp string, speed export.SpeedUnit, cleanup bool, err error) {
	dest = c.Dest
	if dest == "" {
		dest = "~/exotherm"
	}
	dest, err = Expand(dest, home)
	if err != nil {
		return "", "", "", false, err
	}
	tmp = c.Tmp
	if tmp == "" {
		tmp = filepath.Join(dest, "tmp")
	} else {
		tmp, err = Expand(tmp, home)
		if err != nil {
			return "", "", "", false, err
		}
	}
	speed, err = export.ParseSpeedUnit(c.Speed)
	if err != nil {
		return "", "", "", false, err
	}
	cleanup = c.Cleanup == nil || *c.Cleanup
	if filepath.Clean(dest) == filepath.Clean(tmp) {
		return "", "", "", false, fmt.Errorf("tmp and dest are the same path (%s)", dest)
	}
	return dest, tmp, speed, cleanup, nil
}

// Expand replaces a leading ~/ with home and cleans the path.
func Expand(path, home string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home == "" {
			return "", fmt.Errorf("cannot expand %q without a home directory", path)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Clean(path), nil
}
