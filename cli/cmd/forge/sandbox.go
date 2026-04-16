package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

// newSandboxCmd creates the "sandbox" parent command with list/inspect/logs/restart/destroy subcommands.
func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes",
	}

	cmd.AddCommand(newSandboxListCmd())
	cmd.AddCommand(newSandboxInspectCmd())
	cmd.AddCommand(newSandboxLogsCmd())
	cmd.AddCommand(newSandboxRestartCmd())
	cmd.AddCommand(newSandboxDestroyCmd())

	return cmd
}

// ── sandbox list ───────────────────────────────────────────────────────

func newSandboxListCmd() *cobra.Command {
	var (
		appName string
		nodeID  string
		state   string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes with optional filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)

			q := url.Values{}
			if appName != "" {
				q.Set("app_name", appName)
			}
			if nodeID != "" {
				q.Set("node_id", nodeID)
			}
			if state != "" {
				q.Set("state", state)
			}

			body, err := client.get(cmd.Context(), "/v1/sandboxes", q)
			if err != nil {
				return fmt.Errorf("listing sandboxes: %w", err)
			}

			var resp struct {
				Sandboxes []struct {
					ID      string `json:"id"`
					AppName string `json:"app_name"`
					State   string `json:"state"`
					NodeID  string `json:"node_id"`
					URL     string `json:"url"`
				} `json:"sandboxes"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			headers := []string{"ID", "APP_NAME", "STATE", "NODE", "URL"}
			rows := make([][]string, len(resp.Sandboxes))
			for i, s := range resp.Sandboxes {
				rows[i] = []string{
					truncateID(s.ID),
					s.AppName,
					s.State,
					truncateID(s.NodeID),
					s.URL,
				}
			}

			printTable(cmd.OutOrStdout(), headers, rows)
			return nil
		},
	}

	cmd.Flags().StringVar(&appName, "app", "", "Filter by app name")
	cmd.Flags().StringVar(&nodeID, "node", "", "Filter by node ID")
	cmd.Flags().StringVar(&state, "state", "", "Filter by state (pending, running, failed, etc.)")

	return cmd
}

// ── sandbox inspect ────────────────────────────────────────────────────

func newSandboxInspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <id-or-app-name>",
		Short: "Show full sandbox details as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)
			id := args[0]

			body, err := client.get(cmd.Context(), "/v1/sandboxes/"+id, nil)
			if err != nil {
				return fmt.Errorf("inspecting sandbox: %w", err)
			}

			// Pretty-print JSON
			var raw json.RawMessage
			if err := json.Unmarshal(body, &raw); err != nil {
				// If not valid JSON, print as-is
				fmt.Fprint(cmd.OutOrStdout(), string(body))
				return nil
			}

			pretty, err := json.MarshalIndent(raw, "", "  ")
			if err != nil {
				fmt.Fprint(cmd.OutOrStdout(), string(body))
				return nil
			}

			fmt.Fprintln(cmd.OutOrStdout(), string(pretty))
			return nil
		},
	}
}

// ── sandbox logs ───────────────────────────────────────────────────────

func newSandboxLogsCmd() *cobra.Command {
	var (
		follow bool
		tail   int
	)

	cmd := &cobra.Command{
		Use:   "logs <id-or-app-name>",
		Short: "Stream sandbox logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)
			id := args[0]

			q := url.Values{}
			q.Set("tail", strconv.Itoa(tail))
			if follow {
				q.Set("follow", "true")
			}

			body, err := client.getRaw(cmd.Context(), "/v1/sandboxes/"+id+"/logs", q)
			if err != nil {
				return fmt.Errorf("fetching logs: %w", err)
			}

			fmt.Fprint(cmd.OutOrStdout(), string(body))
			return nil
		},
	}

	cmd.Flags().BoolVar(&follow, "follow", false, "Follow log output")
	cmd.Flags().IntVar(&tail, "tail", 100, "Number of lines to show from the end")

	return cmd
}

// ── sandbox restart ────────────────────────────────────────────────────

func newSandboxRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <id>",
		Short: "Force restart a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)
			id := args[0]

			_, err := client.post(cmd.Context(), "/v1/sandboxes/"+id+"/restart", nil)
			if err != nil {
				return fmt.Errorf("restarting sandbox: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Sandbox %s restart queued\n", id)
			return nil
		},
	}
}

// ── sandbox destroy ────────────────────────────────────────────────────

func newSandboxDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <id>",
		Short: "Destroy a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)
			id := args[0]

			if err := client.del(cmd.Context(), "/v1/sandboxes/"+id); err != nil {
				return fmt.Errorf("destroying sandbox: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Sandbox %s destruction queued\n", id)
			return nil
		},
	}
}
