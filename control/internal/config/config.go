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

	CaddyAdminURL string `envconfig:"FORGE_CADDY_ADMIN_URL" default:"http://localhost:2019"`

	IdleReaperIntervalSeconds    int `envconfig:"FORGE_IDLE_REAPER_INTERVAL_SECONDS" default:"60"`
	DriftDetectorIntervalSeconds int `envconfig:"FORGE_DRIFT_DETECTOR_INTERVAL_SECONDS" default:"60"`

	// Phase 30 — read-through freshness threshold. A row is considered
	// fresh if verified_at is within this window; older rows trigger a
	// synchronous agent.ContainerExists call.
	FreshnessWindowSeconds int `envconfig:"FORGE_FRESHNESS_WINDOW_SECONDS" default:"10"`

	// Phase 30 — HTTP timeout on the control plane → agent containers
	// query. Kept short because the call is on the GET /sandboxes
	// critical path; agent unreachable falls through to cached row.
	AgentRequestTimeoutSeconds int `envconfig:"FORGE_AGENT_REQUEST_TIMEOUT_SECONDS" default:"3"`

	// Phase 33-B — sandbox state-change webhook. Empty URL disables the
	// webhook entirely (backwards compat for environments without a
	// listener configured). When set, control plane POSTs JSON to this
	// URL on every sandbox state transition that crosses into running,
	// and HMAC-signs the body with WebhookSecret. Failures are logged
	// and never propagated — the listener is treated as best-effort.
	WebhookURL                string `envconfig:"FORGE_WEBHOOK_URL" default:""`
	WebhookSecret             string `envconfig:"FORGE_WEBHOOK_SECRET" default:""`
	WebhookTimeoutSeconds     int    `envconfig:"FORGE_WEBHOOK_TIMEOUT_SECONDS" default:"3"`
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
