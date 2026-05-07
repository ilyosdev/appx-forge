package main

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

// newRoutesCmd creates the "routes" parent command with list/verify subcommands.
func newRoutesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "routes",
		Short: "Manage routing table",
	}

	cmd.AddCommand(newRoutesListCmd())
	cmd.AddCommand(newRoutesVerifyCmd())

	return cmd
}

// ── routes list ────────────────────────────────────────────────────────

func newRoutesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active routes",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)

			body, err := client.get(cmd.Context(), "/v1/routes", nil)
			if err != nil {
				return fmt.Errorf("listing routes: %w", err)
			}

			var resp struct {
				Routes []struct {
					AppName   string `json:"app_name"`
					SandboxID string `json:"sandbox_id"`
					Upstream  string `json:"upstream"`
				} `json:"routes"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			headers := []string{"APP_NAME", "SANDBOX_ID", "UPSTREAM"}
			rows := make([][]string, len(resp.Routes))
			for i, r := range resp.Routes {
				rows[i] = []string{
					r.AppName,
					truncateID(r.SandboxID),
					r.Upstream,
				}
			}

			printTable(cmd.OutOrStdout(), headers, rows)
			return nil
		},
	}
}

// ── routes verify ──────────────────────────────────────────────────────

func newRoutesVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Diff routes vs running sandboxes to detect drift",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)
			out := cmd.OutOrStdout()

			// Fetch routes
			routeBody, err := client.get(cmd.Context(), "/v1/routes", nil)
			if err != nil {
				return fmt.Errorf("fetching routes: %w", err)
			}

			var routeResp struct {
				Routes []struct {
					AppName   string `json:"app_name"`
					SandboxID string `json:"sandbox_id"`
					Upstream  string `json:"upstream"`
				} `json:"routes"`
			}
			if err := json.Unmarshal(routeBody, &routeResp); err != nil {
				return fmt.Errorf("parsing routes: %w", err)
			}

			// Fetch running sandboxes
			q := url.Values{}
			q.Set("state", "running")
			sandboxBody, err := client.get(cmd.Context(), "/v1/sandboxes", q)
			if err != nil {
				return fmt.Errorf("fetching sandboxes: %w", err)
			}

			var sandboxResp struct {
				Sandboxes []struct {
					ID      string `json:"id"`
					AppName string `json:"app_name"`
					State   string `json:"state"`
				} `json:"sandboxes"`
			}
			if err := json.Unmarshal(sandboxBody, &sandboxResp); err != nil {
				return fmt.Errorf("parsing sandboxes: %w", err)
			}

			// Build lookup sets
			routedApps := make(map[string]bool)
			for _, r := range routeResp.Routes {
				routedApps[r.AppName] = true
			}

			runningApps := make(map[string]bool)
			for _, s := range sandboxResp.Sandboxes {
				runningApps[s.AppName] = true
			}

			// Find discrepancies
			type drift struct {
				AppName string
				Kind    string // "orphan route" or "missing route"
			}
			var drifts []drift

			// Orphan routes: route exists, no running sandbox
			for _, r := range routeResp.Routes {
				if !runningApps[r.AppName] {
					drifts = append(drifts, drift{AppName: r.AppName, Kind: "orphan route"})
				}
			}

			// Missing routes: running sandbox, no route
			for _, s := range sandboxResp.Sandboxes {
				if !routedApps[s.AppName] {
					drifts = append(drifts, drift{AppName: s.AppName, Kind: "missing route"})
				}
			}

			if len(drifts) == 0 {
				fmt.Fprintln(out, "No drift detected -- routes are clean")
				return nil
			}

			// Print drift table
			headers := []string{"APP_NAME", "ISSUE"}
			rows := make([][]string, len(drifts))
			for i, d := range drifts {
				rows[i] = []string{d.AppName, d.Kind}
			}
			printTable(out, headers, rows)

			return fmt.Errorf("found %d route drift(s)", len(drifts))
		},
	}
}
