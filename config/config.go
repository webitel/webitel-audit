package config

import (
	"fmt"
	"log/slog"

	"github.com/spf13/pflag"

	"github.com/webitel/webitel-go-kit/appconfig"
	"github.com/webitel/webitel-go-kit/pkg/errors"
)

type Config struct {
	Service ServiceConfig `mapstructure:"service"`
	Log     appconfig.Log `mapstructure:"log"`
	Health  HealthConfig  `mapstructure:"health"`
}

type ServiceConfig struct {
	// Addr is the gRPC listen address.
	Addr string `mapstructure:"addr"`
}

// HealthConfig exposes the health probes.
type HealthConfig struct {
	// Addr of the /livez /readyz /healthz server. Not set disables it.
	Addr string `mapstructure:"addr"`
}

// LoadServerConfig loads the full configuration required by the server command.
func LoadServerConfig() (*Config, error) {
	loader := appconfig.NewLoader(appconfig.Sections{Log: true})
	loader.RegisterFlags(pflag.CommandLine)
	registerServiceFlags()
	pflag.Parse()

	cfg := &Config{}
	if err := loader.Load(pflag.CommandLine, cfg); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadMigrateConfig loads the minimal configuration required by the migrate command.
func LoadMigrateConfig() (*Config, error) {
	loader := appconfig.NewLoader(appconfig.Sections{Log: true})
	loader.RegisterFlags(pflag.CommandLine)
	pflag.Parse()

	cfg := &Config{}
	if err := loader.Load(pflag.CommandLine, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func registerServiceFlags() {
	pflag.String("service.addr", "127.0.0.1:8080", "gRPC listen address")
	pflag.String("health.addr", "", "listen address of the health probes (empty disables them)")
}

func (c *Config) validate() error {
	if c.Service.Addr == "" {
		return errors.New("config: service.addr is required")
	}

	if c.Log.Level == "" {
		c.Log.Level = "info"
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(c.Log.Level)); err != nil {
		return fmt.Errorf("config: log.level %q is invalid (use debug, info, warn or error)", c.Log.Level)
	}

	return nil
}
