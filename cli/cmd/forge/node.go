package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// newNodeCmd creates the "node" parent command with list/add/drain/remove subcommands.
func newNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage fleet nodes",
	}

	cmd.AddCommand(newNodeListCmd())
	cmd.AddCommand(newNodeAddCmd())
	cmd.AddCommand(newNodeDrainCmd())
	cmd.AddCommand(newNodeRemoveCmd())

	return cmd
}

// ── node list ──────────────────────────────────────────────────────────

func newNodeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all registered nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)

			body, err := client.get(cmd.Context(), "/v1/nodes", nil)
			if err != nil {
				return fmt.Errorf("listing nodes: %w", err)
			}

			var resp struct {
				Nodes []struct {
					ID               string `json:"id"`
					Hostname         string `json:"hostname"`
					Status           string `json:"status"`
					CapacityMb       int    `json:"capacity_mb"`
					UsedMb           int    `json:"used_mb"`
					RunningSandboxes int    `json:"running_sandboxes"`
					AgentVersion     string `json:"agent_version"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			headers := []string{"ID", "HOSTNAME", "STATUS", "CAPACITY", "USED", "SANDBOXES", "VERSION"}
			rows := make([][]string, len(resp.Nodes))
			for i, n := range resp.Nodes {
				rows[i] = []string{
					truncateID(n.ID),
					n.Hostname,
					n.Status,
					strconv.Itoa(n.CapacityMb),
					strconv.Itoa(n.UsedMb),
					strconv.Itoa(n.RunningSandboxes),
					n.AgentVersion,
				}
			}

			printTable(cmd.OutOrStdout(), headers, rows)
			return nil
		},
	}
}

// ── node add ───────────────────────────────────────────────────────────

func newNodeAddCmd() *cobra.Command {
	var (
		hostname    string
		tailscaleIP string
		capacityMb  int
		agentVer    string
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a new node",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)

			reqBody := map[string]any{
				"hostname":      hostname,
				"tailscale_ip":  tailscaleIP,
				"capacity_mb":   capacityMb,
				"agent_version": agentVer,
			}

			body, err := client.post(cmd.Context(), "/v1/nodes/register", reqBody)
			if err != nil {
				return fmt.Errorf("registering node: %w", err)
			}

			var resp struct {
				NodeID                   string `json:"node_id"`
				AgentToken               string `json:"agent_token"`
				HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Node registered successfully\n")
			fmt.Fprintf(out, "  Node ID:    %s\n", resp.NodeID)
			fmt.Fprintf(out, "  Heartbeat:  %ds\n", resp.HeartbeatIntervalSeconds)

			return nil
		},
	}

	cmd.Flags().StringVar(&hostname, "hostname", "", "Node hostname (required)")
	cmd.Flags().StringVar(&tailscaleIP, "tailscale-ip", "", "Tailscale IP address (required)")
	cmd.Flags().IntVar(&capacityMb, "capacity-mb", 0, "Memory capacity in MB (required)")
	cmd.Flags().StringVar(&agentVer, "agent-version", "", "Agent version (required)")

	cmd.MarkFlagRequired("hostname")
	cmd.MarkFlagRequired("tailscale-ip")
	cmd.MarkFlagRequired("capacity-mb")
	cmd.MarkFlagRequired("agent-version")

	return cmd
}

// ── node drain ─────────────────────────────────────────────────────────

func newNodeDrainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drain <node-id>",
		Short: "Set a node to draining status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)
			nodeID := args[0]

			_, err := client.post(cmd.Context(), "/v1/nodes/"+nodeID+"/drain", nil)
			if err != nil {
				return fmt.Errorf("draining node: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Node %s set to draining\n", nodeID)
			return nil
		},
	}
}

// ── node remove ────────────────────────────────────────────────────────

func newNodeRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <node-id>",
		Short: "Remove a node (must have 0 sandboxes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)
			nodeID := args[0]

			if err := client.del(cmd.Context(), "/v1/nodes/"+nodeID); err != nil {
				return fmt.Errorf("removing node: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Node %s removed\n", nodeID)
			return nil
		},
	}
}
