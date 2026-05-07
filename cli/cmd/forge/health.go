package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newHealthCmd creates the "healthcheck" command.
// Per OpenAPI spec, /v1/healthz requires no authentication.
func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Show control plane health status",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)

			// Use getNoAuth since /v1/healthz is an unauthenticated endpoint
			body, err := client.getNoAuth(cmd.Context(), "/v1/healthz")
			if err != nil {
				return fmt.Errorf("checking health: %w", err)
			}

			var resp struct {
				Status        string `json:"status"`
				Postgres      string `json:"postgres"`
				UptimeSeconds int    `json:"uptime_seconds"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			out := cmd.OutOrStdout()

			// Format status indicators
			cpStatus := "OK"
			if resp.Status != "ok" {
				cpStatus = "FAIL"
			}
			pgStatus := "OK"
			if resp.Postgres != "ok" {
				pgStatus = "FAIL"
			}

			fmt.Fprintf(out, "Control Plane: %s\n", cpStatus)
			fmt.Fprintf(out, "Postgres:      %s\n", pgStatus)
			fmt.Fprintf(out, "Uptime:        %s\n", formatUptime(resp.UptimeSeconds))

			// Return error if unhealthy (exit code 1)
			if resp.Status != "ok" {
				return fmt.Errorf("control plane unhealthy: status=%s", resp.Status)
			}

			return nil
		},
	}
}

// formatUptime converts seconds to a human-readable duration string.
func formatUptime(seconds int) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60

	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case h > 0:
		return fmt.Sprintf("%dh %ds", h, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
