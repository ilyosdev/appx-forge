package main

import (
	"github.com/spf13/cobra"
)

// newRootCmd creates the root forge command with global flags.
// Using a constructor instead of a global var allows tests to get fresh instances.
func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "forge",
		Short: "Forge fleet management CLI",
	}

	// Global persistent flags
	cmd.PersistentFlags().String("api-url", "", "Control plane API URL (env: FORGE_API_URL)")
	cmd.PersistentFlags().String("api-token", "", "API bearer token (env: FORGE_API_TOKEN)")
	cmd.PersistentFlags().StringP("output", "o", "table", "Output format: table or json")

	return cmd
}
