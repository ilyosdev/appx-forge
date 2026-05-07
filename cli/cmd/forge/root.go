package main

import (
	"os"

	"github.com/spf13/cobra"
)

// newRootCmd creates the root forge command with global flags.
// Using a constructor instead of a global var allows tests to get fresh instances.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "forge",
		Short: "Forge fleet management CLI",
		SilenceUsage: true,
	}

	// Global persistent flags with env var defaults
	cmd.PersistentFlags().String("api-url", envOrDefault("FORGE_API_URL", "http://localhost:8080"), "Control plane API URL (env: FORGE_API_URL)")
	cmd.PersistentFlags().String("api-token", envOrDefault("FORGE_API_TOKEN", ""), "API bearer token (env: FORGE_API_TOKEN)")
	cmd.PersistentFlags().StringP("output", "o", "table", "Output format: table or json")

	// Register subcommands
	cmd.AddCommand(newNodeCmd())
	cmd.AddCommand(newSandboxCmd())
	cmd.AddCommand(newRoutesCmd())
	cmd.AddCommand(newEventsCmd())
	cmd.AddCommand(newHealthCmd())

	return cmd
}

// resolveClient reads global flags and creates an apiClient.
func resolveClient(cmd *cobra.Command) *apiClient {
	apiURL, _ := cmd.Flags().GetString("api-url")
	apiToken, _ := cmd.Flags().GetString("api-token")
	return newAPIClient(apiURL, apiToken)
}

// envOrDefault returns the env var value if set, otherwise the fallback.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
