// Package migrations bundles the forge-control SQL migrations into the binary
// via go:embed. The control plane uses this FS via goose.SetBaseFS so that
// scp-based deploys ship migrations alongside the binary automatically.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
