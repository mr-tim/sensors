// Package config loads the application's settings from environment variables.
package config

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config holds every setting the application needs. Each field is read
// from the environment variable named by its `envconfig` tag.
type Config struct {
	EwelinkAppID     string `envconfig:"EWELINK_APP_ID" required:"true"`
	EwelinkAppSecret string `envconfig:"EWELINK_APP_SECRET" required:"true"`
	DatabaseURL      string `envconfig:"DATABASE_URL" required:"true"`

	// Heatmiser NeoHub (optional: thermostats are only monitored when a
	// token is set). Create a token in the Neo app under Settings > API.
	NeoHubHost         string        `envconfig:"NEOHUB_HOST" default:"Neo-Hub"`
	NeoHubPort         int           `envconfig:"NEOHUB_PORT" default:"4243"`
	NeoHubToken        string        `envconfig:"NEOHUB_TOKEN"`
	NeoHubPollInterval time.Duration `envconfig:"NEOHUB_POLL_INTERVAL" default:"1m"`
}

// Load reads the configuration from the environment.
func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
