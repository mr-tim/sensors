// Package config loads the application's settings from environment variables.
package config

import "github.com/kelseyhightower/envconfig"

// Config holds every setting the application needs. Each field is read
// from the environment variable named by its `envconfig` tag.
type Config struct {
	EwelinkAppID     string `envconfig:"EWELINK_APP_ID" required:"true"`
	EwelinkAppSecret string `envconfig:"EWELINK_APP_SECRET" required:"true"`
	DatabaseURL      string `envconfig:"DATABASE_URL" required:"true"`
}

// Load reads the configuration from the environment.
func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
