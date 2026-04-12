package config

import (
	"os"
	"path/filepath"

	"hop.top/kit/config"
	"hop.top/kit/xdg"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Model        string `yaml:"model"`
	PatternsPath string `yaml:"patterns_path"`
	Accent       string `yaml:"accent"`
}

func Load() (Config, error) {
	var cfg Config
	// Defaults
	cfg.Model = "claude-3-5-sonnet-latest"
	cfg.Accent = "#E040FB"

	confDir, _ := xdg.ConfigDir("foo")
	userConfig := filepath.Join(confDir, "config.yaml")
	projectConfig := ".foo.yaml"

	err := config.Load(&cfg, config.Options{
		UserConfigPath:    userConfig,
		ProjectConfigPath: projectConfig,
	})

	if cfg.PatternsPath == "" {
		cfg.PatternsPath = filepath.Join(confDir, "patterns")
	}

	return cfg, err
}

func (c Config) Save() error {
	confDir, err := xdg.ConfigDir("foo")
	if err != nil {
		return err
	}
	userConfig := filepath.Join(confDir, "config.yaml")
	
	if err := os.MkdirAll(filepath.Dir(userConfig), 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(userConfig, data, 0644)
}
