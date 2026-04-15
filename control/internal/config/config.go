// Package config provides environment-based configuration for the control plane.
package config

import "github.com/kelseyhightower/envconfig"

// Config holds all control plane configuration. Fields are populated from
// environment variables using the FORGE_ prefix via envconfig struct tags.
type Config struct {
	ListenAddr  string `envconfig:"FORGE_LISTEN_ADDR" default:":8080"`
	DatabaseURL string `envconfig:"FORGE_DATABASE_URL" required:"true"`
	APIToken    string `envconfig:"FORGE_API_TOKEN" required:"true"`
	HMACSecret  string `envconfig:"FORGE_HMAC_SECRET" required:"true"`
	LogLevel    string `envconfig:"FORGE_LOG_LEVEL" default:"info"`

	HeartbeatIntervalSeconds int `envconfig:"FORGE_HEARTBEAT_INTERVAL_SECONDS" default:"15"`
	HeartbeatMissThreshold   int `envconfig:"FORGE_HEARTBEAT_MISS_THRESHOLD" default:"3"`
}

// Load parses environment variables into a Config struct. Returns an error
// if any required field is missing.
func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
