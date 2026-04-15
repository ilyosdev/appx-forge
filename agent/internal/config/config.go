package config

import "github.com/kelseyhightower/envconfig"

// Config holds all agent configuration loaded from environment variables.
// All fields use the FORGE_ prefix via envconfig struct tags.
//
// Required fields: FORGE_CONTROL_URL, FORGE_HOSTNAME, FORGE_HMAC_SECRET.
// All other fields have sensible defaults.
type Config struct {
	// ControlURL is the base URL of the forge control plane.
	ControlURL string `envconfig:"FORGE_CONTROL_URL" required:"true"`

	// Hostname identifies this node in the control plane.
	Hostname string `envconfig:"FORGE_HOSTNAME" required:"true"`

	// TailscaleIP is the node's Tailscale address. Auto-detected if empty.
	TailscaleIP string `envconfig:"FORGE_TAILSCALE_IP"`

	// AgentPort is the HTTP listen port for the agent's own server.
	AgentPort int `envconfig:"FORGE_AGENT_PORT" default:"8090"`

	// PortRangeMin is the lower bound (inclusive) of the host port range for sandbox containers.
	PortRangeMin int `envconfig:"FORGE_PORT_RANGE_MIN" default:"40000"`

	// PortRangeMax is the upper bound (inclusive) of the host port range for sandbox containers.
	PortRangeMax int `envconfig:"FORGE_PORT_RANGE_MAX" default:"50000"`

	// HMACSecret is the shared key for signing file push URLs.
	// SECURITY: Never log this value.
	HMACSecret string `envconfig:"FORGE_HMAC_SECRET" required:"true"`

	// SandboxDir is the base directory for sandbox bind mounts.
	SandboxDir string `envconfig:"FORGE_SANDBOX_DIR" default:"/var/lib/forge/sandboxes"`

	// AgentVersion is reported to the control plane during registration.
	AgentVersion string `envconfig:"FORGE_AGENT_VERSION" default:"0.1.0"`

	// SandboxImage is the default Docker image to pre-pull for sandboxes.
	SandboxImage string `envconfig:"FORGE_SANDBOX_IMAGE" default:"appx/sandbox:v1"`
}

// Load reads configuration from environment variables.
// It returns an error if any required variable is missing.
func Load() (*Config, error) {
	var cfg Config
	// Empty prefix because envconfig tags already contain full FORGE_ names.
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
