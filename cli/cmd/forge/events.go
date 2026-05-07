package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// newEventsCmd creates the "events" command for viewing event history.
func newEventsCmd() *cobra.Command {
	var (
		sandboxID string
		eventType string
		since     string
		limit     int
	)

	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show event history",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := resolveClient(cmd)

			q := url.Values{}
			q.Set("limit", strconv.Itoa(limit))

			if sandboxID != "" {
				q.Set("sandbox_id", sandboxID)
			}
			if eventType != "" {
				q.Set("event_type", eventType)
			}

			body, err := client.get(cmd.Context(), "/v1/events", q)
			if err != nil {
				return fmt.Errorf("fetching events: %w", err)
			}

			var resp struct {
				Events []struct {
					ID        int    `json:"id"`
					SandboxID string `json:"sandbox_id"`
					EventType string `json:"event_type"`
					PrevState string `json:"prev_state"`
					NextState string `json:"next_state"`
					CreatedAt string `json:"created_at"`
				} `json:"events"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			// Apply client-side --since filter
			var sinceTime time.Time
			if since != "" {
				dur, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("invalid --since duration %q: %w", since, err)
				}
				sinceTime = time.Now().UTC().Add(-dur)
			}

			headers := []string{"TIME", "TYPE", "SANDBOX_ID", "TRANSITION"}
			var rows [][]string

			for _, e := range resp.Events {
				// Parse event timestamp
				var eventTime time.Time
				if e.CreatedAt != "" {
					t, err := time.Parse("2006-01-02T15:04:05Z", e.CreatedAt)
					if err == nil {
						eventTime = t
					}
				}

				// Apply --since filter
				if !sinceTime.IsZero() && !eventTime.IsZero() && eventTime.Before(sinceTime) {
					continue
				}

				// Format relative time
				timeStr := e.CreatedAt
				if !eventTime.IsZero() {
					timeStr = formatRelativeTime(eventTime)
				}

				// Format transition
				transition := ""
				if e.PrevState != "" || e.NextState != "" {
					transition = e.PrevState + " -> " + e.NextState
				}

				rows = append(rows, []string{
					timeStr,
					e.EventType,
					truncateID(e.SandboxID),
					transition,
				})
			}

			printTable(cmd.OutOrStdout(), headers, rows)
			return nil
		},
	}

	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "Filter by sandbox UUID")
	cmd.Flags().StringVar(&eventType, "type", "", "Filter by event type")
	cmd.Flags().StringVar(&since, "since", "", "Show events newer than duration (e.g., 5m, 1h, 24h)")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of events to fetch")

	return cmd
}

// formatRelativeTime returns a human-readable relative time string (e.g., "2m ago").
func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = -d
	}

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(math.Round(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(math.Round(d.Minutes())))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh%dm ago", h, m)
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	}
}
