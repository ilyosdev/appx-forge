package main

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// printTable writes a formatted table to w using tabwriter.
// headers and rows use tab-separated columns.
func printTable(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// Print header row
	for i, h := range headers {
		if i > 0 {
			fmt.Fprint(tw, "\t")
		}
		fmt.Fprint(tw, h)
	}
	fmt.Fprintln(tw)

	// Print data rows
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, cell)
		}
		fmt.Fprintln(tw)
	}

	tw.Flush()
}

// truncateID returns the first 8 characters of a UUID string for compact display.
func truncateID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
