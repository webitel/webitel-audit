package config

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"

	"github.com/spf13/pflag"

	"github.com/webitel/webitel-go-kit/appconfig"
	"github.com/webitel/webitel-go-kit/pkg/errors"
)

type Config struct {
	Service  ServiceConfig      `mapstructure:"service"`
	Log      appconfig.Log      `mapstructure:"log"`
	Postgres appconfig.Postgres `mapstructure:"postgres"`
	Consul   appconfig.Consul   `mapstructure:"consul"`
	Health   HealthConfig       `mapstructure:"health"`
}

type ServiceConfig struct {
	// Addr is the gRPC listen address.
	Addr string `mapstructure:"addr"`
	// Connection holds the gRPC TLS settings.
	Connection appconfig.GRPCConn `mapstructure:"conn"`
}

// HealthConfig exposes the health probes.
type HealthConfig struct {
	// Addr of the /livez /readyz /healthz server. Not set disables it.
	Addr string `mapstructure:"addr"`
}

// LoadServerConfig loads the full configuration required by the server command.
func LoadServerConfig() (*Config, error) {
	loader := appconfig.NewLoader(appconfig.Sections{Log: true, Postgres: true, Consul: true})
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
	loader := appconfig.NewLoader(appconfig.Sections{Log: true, Postgres: true})
	loader.RegisterFlags(pflag.CommandLine)
	pflag.Parse()

	cfg := &Config{}
	if err := loader.Load(pflag.CommandLine, cfg); err != nil {
		return nil, err
	}

	if err := cfg.validateMigrate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func registerServiceFlags() {
	pflag.String("service.addr", "127.0.0.1:8080", "gRPC listen address, registered in Consul as is")
	appconfig.RegisterGRPCConnFlags(pflag.CommandLine, "service.conn", true)
	pflag.String("health.addr", "", "listen address of the health probes (empty disables them)")
}

func (c *Config) validate() error {
	if c.Service.Addr == "" {
		return errors.New("config: service.addr is required")
	}

	host, port, err := net.SplitHostPort(c.Service.Addr)
	if err != nil {
		return fmt.Errorf("config: service.addr %q is invalid: %w", c.Service.Addr, err)
	}

	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		return fmt.Errorf("config: service.addr %q needs a concrete host, it is registered in Consul", c.Service.Addr)
	}

	if p, err := strconv.ParseUint(port, 10, 16); err != nil || p == 0 {
		return fmt.Errorf("config: service.addr %q needs a numeric port above 0, it is registered in Consul", c.Service.Addr)
	}

	if c.Consul.Addr == "" {
		return errors.New("config: consul.addr is required")
	}

	if err := appconfig.ValidateGRPCConn("service.conn", c.Service.Connection); err != nil {
		return err
	}

	return c.validateMigrate()
}

func (c *Config) validateMigrate() error {
	if c.Postgres.DSN == "" {
		return errors.New("config: postgres.dsn is required")
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
