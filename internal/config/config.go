package config

import (
	"os"
	"path/filepath"
	"strings"

	"hop.top/kit/go/core/config"
	"hop.top/kit/go/core/xdg"
	"hop.top/kit/go/storage/secret"
	// The backends `secrets.backend` accepts: kit registers each one
	// through a blank import, so a name missing here opens as "unknown
	// backend". env is the default; keyring is the OS keychain.
	_ "hop.top/kit/go/storage/secret/env"
	_ "hop.top/kit/go/storage/secret/keyring"
)

const (
	DefaultModel  = "claude-3-5-sonnet-latest"
	DefaultAccent = "#E040FB"
	// DefaultKeyringService is the keychain service the keyring secrets
	// backend uses when `secrets.service` is empty.
	DefaultKeyringService = "foo"
)

type Secrets struct {
	Backend string `yaml:"backend"`
	Prefix  string `yaml:"prefix"`
	Service string `yaml:"service"`
}

type Config struct {
	Model        string `yaml:"model"`
	PatternsPath string `yaml:"patterns_path"`
	Accent       string `yaml:"accent"`
	// Budget is the default pool routing tier (cheap|balanced|premium).
	// Empty means "no config-file preference"; ResolveBudget falls
	// through to the env var or the kit default. Validated at use,
	// not at load, so a misspell on disk does not block startup.
	Budget  string  `yaml:"budget"`
	Secrets Secrets `yaml:"secrets"`
}

func Default() Config {
	return Config{
		Model:  DefaultModel,
		Accent: DefaultAccent,
		Secrets: Secrets{
			Backend: "env",
			Prefix:  "",
		},
	}
}

// LoadOptions carries the bits of kit/cli config-arg state that the
// loader needs. ExtraConfigPaths layers extra files after the discovered
// ones; Overrides is applied last and wins over every other layer.
type LoadOptions struct {
	ExtraConfigPaths []string
	Overrides        map[string]any
}

func Load(opts LoadOptions) (Config, error) {
	cfg := Default()
	confDir, _ := xdg.ConfigDir("foo")
	userConfig := filepath.Join(confDir, "config.yaml")
	projectConfig := ".foo.yaml"
	systemConfig := "/etc/foo/config.yaml"

	err := config.Load(&cfg, config.Options{
		SystemConfigPath:  systemConfig,
		UserConfigPath:    userConfig,
		ProjectConfigPath: projectConfig,
		ExtraConfigPaths:  opts.ExtraConfigPaths,
		EnvOverride:       applyEnv,
		Overrides:         opts.Overrides,
	})

	if cfg.PatternsPath == "" {
		cfg.PatternsPath = filepath.Join(confDir, "patterns")
	}
	if cfg.Secrets.Backend == "" {
		cfg.Secrets.Backend = "env"
	}

	return cfg, err
}

// SecretStore opens the store the `secrets:` block configures. Every
// provider key lookup reads it: the run's precheck and fallbacks
// (llm.ClientOpts.Secrets) and the credential index behind
// `foo provider show` and `foo model list`.
//
// A keyring store with no service (none in config, no --profile) uses
// DefaultKeyringService rather than kit's shared "kit", so foo's keys
// stay under foo's own name.
func (c Config) SecretStore() (secret.MutableStore, error) {
	service := c.Secrets.Service
	if c.Secrets.Backend == "keyring" && service == "" {
		service = DefaultKeyringService
	}
	return secret.Open(secret.Config{
		Backend: c.Secrets.Backend,
		Prefix:  c.Secrets.Prefix,
		Service: service,
	})
}

func applyEnv(cfg any) {
	c, ok := cfg.(*Config)
	if !ok {
		return
	}
	apply := func(env string, set func(string)) {
		if value, ok := os.LookupEnv(env); ok && strings.TrimSpace(value) != "" {
			set(value)
		}
	}

	apply("FOO_MODEL", func(v string) { c.Model = v })
	apply("FOO_PATTERNS_PATH", func(v string) { c.PatternsPath = v })
	apply("FOO_ACCENT", func(v string) { c.Accent = v })
	apply("FOO_BUDGET", func(v string) { c.Budget = v })
	apply("FOO_SECRETS_BACKEND", func(v string) { c.Secrets.Backend = v })
	apply("FOO_SECRETS_PREFIX", func(v string) { c.Secrets.Prefix = v })
	apply("FOO_SECRETS_SERVICE", func(v string) { c.Secrets.Service = v })
}
